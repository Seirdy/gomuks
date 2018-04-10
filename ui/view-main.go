// gomuks - A terminal Matrix client written in Go.
// Copyright (C) 2018 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell"
	"github.com/mattn/go-runewidth"
	"maunium.net/go/gomatrix"
	"maunium.net/go/gomuks/config"
	"maunium.net/go/gomuks/debug"
	"maunium.net/go/gomuks/interface"
	"maunium.net/go/gomuks/matrix/pushrules"
	"maunium.net/go/gomuks/matrix/rooms"
	"maunium.net/go/gomuks/notification"
	"maunium.net/go/gomuks/ui/messages"
	"maunium.net/go/gomuks/ui/widget"
	"maunium.net/go/tview"
)

type MainView struct {
	*tview.Flex

	roomList         *RoomList
	roomView         *tview.Pages
	rooms            map[string]*RoomView
	currentRoomIndex int
	roomIDs          []string

	lastFocusTime time.Time

	matrix ifc.MatrixContainer
	gmx    ifc.Gomuks
	config *config.Config
	parent *GomuksUI
}

func (ui *GomuksUI) NewMainView() tview.Primitive {
	mainView := &MainView{
		Flex:     tview.NewFlex(),
		roomList: NewRoomList(),
		roomView: tview.NewPages(),
		rooms:    make(map[string]*RoomView),

		matrix: ui.gmx.Matrix(),
		gmx:    ui.gmx,
		config: ui.gmx.Config(),
		parent: ui,
	}

	mainView.SetDirection(tview.FlexColumn)
	mainView.AddItem(mainView.roomList, 25, 0, false)
	mainView.AddItem(widget.NewBorder(), 1, 0, false)
	mainView.AddItem(mainView.roomView, 0, 1, true)

	ui.mainView = mainView

	return mainView
}

func (view *MainView) BumpFocus() {
	view.lastFocusTime = time.Now()
}

func (view *MainView) InputChanged(roomView *RoomView, text string) {
	if len(text) == 0 {
		go view.matrix.SendTyping(roomView.Room.ID, false)
	} else if text[0] != '/' {
		go view.matrix.SendTyping(roomView.Room.ID, true)
	}
}

func findWordToTabComplete(text string) string {
	output := ""
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			break
		}
		output = string(runes[i]) + output
	}
	return output
}

func (view *MainView) InputTabComplete(roomView *RoomView, text string, cursorOffset int) string {
	str := runewidth.Truncate(text, cursorOffset, "")
	word := findWordToTabComplete(str)
	userCompletions := roomView.AutocompleteUser(word)
	if len(userCompletions) == 1 {
		startIndex := len(str) - len(word)
		completion := userCompletions[0]
		if startIndex == 0 {
			completion = completion + ": "
		}
		text = str[0:startIndex] + completion + text[len(str):]
	} else if len(userCompletions) > 1 && len(userCompletions) < 6 {
		roomView.SetStatus(fmt.Sprintf("Completions: %s", strings.Join(userCompletions, ", ")))
	}
	return text
}

func (view *MainView) InputSubmit(roomView *RoomView, text string) {
	if len(text) == 0 {
		return
	} else if text[0] == '/' {
		args := strings.SplitN(text, " ", 2)
		command := strings.ToLower(args[0])
		args = args[1:]
		go view.HandleCommand(roomView, command, args)
	} else {
		view.SendMessage(roomView, text)
	}
	roomView.SetInputText("")
}

func (view *MainView) SendMessage(roomView *RoomView, text string) {
	tempMessage := roomView.NewTempMessage("m.text", text)
	go view.sendTempMessage(roomView, tempMessage)
}

func (view *MainView) sendTempMessage(roomView *RoomView, tempMessage ifc.Message) {
	defer view.gmx.Recover()
	eventID, err := view.matrix.SendMessage(roomView.Room.ID, tempMessage.Type(), tempMessage.Text())
	if err != nil {
		tempMessage.SetState(ifc.MessageStateFailed)
		roomView.SetStatus(fmt.Sprintf("Failed to send message: %s", err))
	} else {
		roomView.MessageView().UpdateMessageID(tempMessage, eventID)
	}
}

func (view *MainView) HandleCommand(roomView *RoomView, command string, args []string) {
	defer view.gmx.Recover()
	debug.Print("Handling command", command, args)
	switch command {
	case "/me":
		tempMessage := roomView.NewTempMessage("m.emote", strings.Join(args, " "))
		go view.sendTempMessage(roomView, tempMessage)
		view.parent.Render()
	case "/quit":
		view.gmx.Stop()
	case "/clearcache":
		view.config.Clear()
		view.gmx.Stop()
	case "/panic":
		panic("This is a test panic.")
	case "/part":
		fallthrough
	case "/leave":
		debug.Print("Leave room result:", view.matrix.LeaveRoom(roomView.Room.ID))
	case "/join":
		if len(args) == 0 {
			roomView.AddServiceMessage("Usage: /join <room>")
			break
		}
		debug.Print("Join room result:", view.matrix.JoinRoom(args[0]))
	default:
		roomView.AddServiceMessage("Unknown command.")
	}
}

func (view *MainView) KeyEventHandler(roomView *RoomView, key *tcell.EventKey) *tcell.EventKey {
	view.BumpFocus()

	k := key.Key()
	if key.Modifiers() == tcell.ModCtrl || key.Modifiers() == tcell.ModAlt {
		switch k {
		case tcell.KeyDown:
			view.SwitchRoom(view.currentRoomIndex + 1)
		case tcell.KeyUp:
			view.SwitchRoom(view.currentRoomIndex - 1)
		default:
			return key
		}
	} else if k == tcell.KeyPgUp || k == tcell.KeyPgDn || k == tcell.KeyUp || k == tcell.KeyDown || k == tcell.KeyEnd || k == tcell.KeyHome {
		msgView := roomView.MessageView()

		if msgView.IsAtTop() && (k == tcell.KeyPgUp || k == tcell.KeyUp) {
			go view.LoadHistory(roomView.Room.ID, false)
		}

		switch k {
		case tcell.KeyPgUp:
			msgView.AddScrollOffset(msgView.Height() / 2)
		case tcell.KeyPgDn:
			msgView.AddScrollOffset(-msgView.Height() / 2)
		case tcell.KeyUp:
			msgView.AddScrollOffset(1)
		case tcell.KeyDown:
			msgView.AddScrollOffset(-1)
		case tcell.KeyHome:
			msgView.AddScrollOffset(msgView.TotalHeight())
		case tcell.KeyEnd:
			msgView.AddScrollOffset(-msgView.TotalHeight())
		}
	} else {
		return key
	}
	return nil
}

const WheelScrollOffsetDiff = 3

func (view *MainView) MouseEventHandler(roomView *RoomView, event *tcell.EventMouse) *tcell.EventMouse {
	if event.Buttons() == tcell.ButtonNone {
		return event
	}
	view.BumpFocus()

	msgView := roomView.MessageView()
	x, y := event.Position()

	switch event.Buttons() {
	case tcell.WheelUp:
		if msgView.IsAtTop() {
			go view.LoadHistory(roomView.Room.ID, false)
		} else {
			msgView.AddScrollOffset(WheelScrollOffsetDiff)

			view.parent.Render()
		}
	case tcell.WheelDown:
		msgView.AddScrollOffset(-WheelScrollOffsetDiff)

		view.parent.Render()

		if msgView.ScrollOffset == 0 {
			roomView.Room.MarkRead()
		}
	default:
		debug.Print("Mouse event received:", event.Buttons(), event.Modifiers(), x, y)
		return event
	}

	return event
}

func (view *MainView) CurrentRoomID() string {
	if len(view.roomIDs) == 0 {
		return ""
	}
	return view.roomIDs[view.currentRoomIndex]
}

func (view *MainView) SwitchRoom(roomIndex int) {
	if roomIndex < 0 {
		roomIndex = len(view.roomIDs) - 1
	}
	if len(view.roomIDs) == 0 {
		return
	}
	view.currentRoomIndex = roomIndex % len(view.roomIDs)
	view.roomView.SwitchToPage(view.CurrentRoomID())
	roomView := view.rooms[view.CurrentRoomID()]
	if roomView.MessageView().ScrollOffset == 0 {
		roomView.Room.MarkRead()
	}
	view.roomList.SetSelected(roomView.Room)
	view.gmx.App().SetFocus(view)
	view.parent.Render()
}

func (view *MainView) Focus(delegate func(p tview.Primitive)) {
	roomView, ok := view.rooms[view.CurrentRoomID()]
	if ok {
		delegate(roomView)
	}
}

func (view *MainView) SaveAllHistory() {
	for _, room := range view.rooms {
		err := room.SaveHistory(view.config.HistoryDir)
		if err != nil {
			debug.Printf("Failed to save history of %s: %v", room.Room.GetTitle(), err)
		}
	}
}

func (view *MainView) addRoom(index int, room string) {
	roomStore := view.matrix.GetRoom(room)

	view.roomList.Add(roomStore)
	if !view.roomView.HasPage(room) {
		roomView := NewRoomView(roomStore).
			SetInputSubmitFunc(view.InputSubmit).
			SetInputChangedFunc(view.InputChanged).
			SetTabCompleteFunc(view.InputTabComplete).
			SetInputCapture(view.KeyEventHandler).
			SetMouseCapture(view.MouseEventHandler)
		view.rooms[room] = roomView
		view.roomView.AddPage(room, roomView, true, false)
		roomView.UpdateUserList()

		count, err := roomView.LoadHistory(view.config.HistoryDir)
		if err != nil {
			debug.Printf("Failed to load history of %s: %v", roomView.Room.GetTitle(), err)
		} else if count <= 0 {
			go view.LoadHistory(room, true)
		}
	}
}

func (view *MainView) GetRoom(id string) ifc.RoomView {
	return view.rooms[id]
}

func (view *MainView) HasRoom(room string) bool {
	for _, existingRoom := range view.roomIDs {
		if existingRoom == room {
			return true
		}
	}
	return false
}

func (view *MainView) AddRoom(room string) {
	if view.HasRoom(room) {
		return
	}
	view.roomIDs = append(view.roomIDs, room)
	view.addRoom(len(view.roomIDs)-1, room)
}

func (view *MainView) RemoveRoom(room string) {
	roomView := view.GetRoom(room)
	if roomView == nil {
		return
	}
	removeIndex := 0
	if view.CurrentRoomID() == room {
		removeIndex = view.currentRoomIndex
		view.SwitchRoom(view.currentRoomIndex - 1)
	} else {
		removeIndex = sort.StringSlice(view.roomIDs).Search(room)
	}
	view.roomList.Remove(roomView.MxRoom())
	view.roomIDs = append(view.roomIDs[:removeIndex], view.roomIDs[removeIndex+1:]...)
	view.roomView.RemovePage(room)
	delete(view.rooms, room)
	view.parent.Render()
}

func (view *MainView) SetRooms(rooms []string) {
	view.roomIDs = rooms
	view.roomList.Clear()
	view.roomView.Clear()
	view.rooms = make(map[string]*RoomView)
	for index, room := range rooms {
		view.addRoom(index, room)
	}
	view.SwitchRoom(0)
}

func (view *MainView) SetTyping(room string, users []string) {
	roomView, ok := view.rooms[room]
	if ok {
		roomView.SetTyping(users)
		view.parent.Render()
	}
}

func sendNotification(room *rooms.Room, sender, text string, critical, sound bool) {
	if room.GetTitle() != sender {
		sender = fmt.Sprintf("%s (%s)", sender, room.GetTitle())
	}
	notification.Send(sender, text, critical, sound)
}

func (view *MainView) NotifyMessage(room *rooms.Room, message ifc.Message, should pushrules.PushActionArrayShould) {
	// Whether or not the room where the message came is the currently shown room.
	isCurrent := room.ID == view.CurrentRoomID()
	// Whether or not the terminal window is focused.
	isFocused := view.lastFocusTime.Add(30 * time.Second).Before(time.Now())

	// Whether or not the push rules say this message should be notified about.
	shouldNotify := (should.Notify || !should.NotifySpecified) && message.Sender() != view.config.Session.UserID

	if !isCurrent {
		// The message is not in the current room, show new message status in room list.
		room.HasNewMessages = true
		room.Highlighted = should.Highlight || room.Highlighted
		if shouldNotify {
			room.UnreadMessages++
		}
	}

	if shouldNotify && !isFocused {
		// Push rules say notify and the terminal is not focused, send desktop notification.
		shouldPlaySound := should.PlaySound && should.SoundName == "default"
		sendNotification(room, message.Sender(), message.Text(), should.Highlight, shouldPlaySound)
	}

	message.SetIsHighlight(should.Highlight)
}

func (view *MainView) LoadHistory(room string, initial bool) {
	defer view.gmx.Recover()
	roomView := view.rooms[room]

	batch := roomView.Room.PrevBatch
	lockTime := time.Now().Unix() + 1

	roomView.Room.LockHistory()
	roomView.MessageView().LoadingMessages = true
	defer func() {
		roomView.Room.UnlockHistory()
		roomView.MessageView().LoadingMessages = false
	}()

	// There's no clean way to try to lock a mutex, so we just check if we still
	// want to continue after we get the lock. This function should always be ran
	// in a goroutine, so the blocking doesn't matter.
	if time.Now().Unix() >= lockTime || batch != roomView.Room.PrevBatch {
		return
	}

	if initial {
		batch = view.config.Session.NextBatch
		debug.Print("Loading initial history for", room)
	} else {
		debug.Print("Loading more history for", room, "starting from", batch)
	}
	history, prevBatch, err := view.matrix.GetHistory(roomView.Room.ID, batch, 50)
	if err != nil {
		roomView.AddServiceMessage("Failed to fetch history")
		debug.Print("Failed to fetch history for", roomView.Room.ID, err)
		return
	}
	roomView.Room.PrevBatch = prevBatch
	for _, evt := range history {
		message := view.ParseEvent(roomView, &evt)
		if message != nil {
			roomView.AddMessage(message, ifc.PrependMessage)
		}
	}
	err = roomView.SaveHistory(view.config.HistoryDir)
	if err != nil {
		debug.Printf("Failed to save history of %s: %v", roomView.Room.GetTitle(), err)
	}
	view.config.Session.Save()
	view.parent.Render()
}

func (view *MainView) ParseEvent(roomView ifc.RoomView, evt *gomatrix.Event) ifc.Message {
	return messages.ParseEvent(view.matrix, roomView.MxRoom(), evt)
}
