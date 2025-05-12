// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package chatbox

import (
	"context"
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

type Identity string

const (
	User   = Identity("user")
	System = Identity("system")
)

var ErrCloseChat = errors.New("close chat")

type Message struct {
	Who     Identity
	Content string
}

// HandleInputFunc should process `in`, closing `out` once all processing has completed.
// The input handler is responsible for echoing the input into `out` if desired.
// If the handler decides the chat UI should be closed, the handler should return ErrCloseChat
// The chatbox will notify the handler of any cancellation via the ctx.
type ChatBackend interface {
	HandleInput(ctx context.Context, in string, out chan<- *Message) error
}

type ChatBox struct {
	app                 *tview.Application
	widget              *tview.Flex
	history             *tview.TextView
	inputBox            *tview.TextArea
	handler             ChatBackend
	previousInputCancel func()
	exit                chan bool
}

type ChatBoxOpts struct {
	InputHeader string
	Welcome     string
}

func NewChatbox(app *tview.Application, handler ChatBackend, opts ChatBoxOpts) *ChatBox {
	cb := &ChatBox{
		app:      app,
		handler:  handler,
		history:  tview.NewTextView(),
		inputBox: tview.NewTextArea(),
		exit:     make(chan bool),
	}
	if opts.Welcome != "" {
		cb.renderMessage(&Message{Who: System, Content: opts.Welcome})
	}
	cb.history.ScrollToEnd()
	cb.inputBox.SetBorder(true).SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			ctx, cancel := context.WithCancel(context.Background())
			input := cb.inputBox.GetText()
			cb.inputBox.SetText("", true)
			go cb.HandleInput(ctx, input)
			if cb.previousInputCancel != nil {
				cb.previousInputCancel()
			}
			cb.previousInputCancel = cancel
			return nil
		}
		return event
	})
	if opts.InputHeader != "" {
		cb.inputBox.SetTitle(opts.InputHeader)
	}
	flexed := 0
	unit := 1
	focused := true
	cb.widget = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(cb.history, flexed, unit, !focused).
		AddItem(cb.inputBox, 3, 0, focused)
	cb.widget.SetBorder(true)
	return cb
}

func (cb *ChatBox) HandleInput(ctx context.Context, in string) {
	out := make(chan *Message)
	go func() {
		for msg := range out {
			cb.app.QueueUpdateDraw(func() {
				cb.renderMessage(msg)
			})
		}
	}()
	err := cb.handler.HandleInput(ctx, in, out)
	if errors.Is(err, ErrCloseChat) {
		cb.exit <- true
	} else if err != nil {
		go cb.app.QueueUpdateDraw(func() {
			cb.renderMessage(&Message{Who: System, Content: errors.Wrap(err, "handling input").Error()})
		})
	}
}

func (cb *ChatBox) renderMessage(msg *Message) {
	cb.history.Write([]byte(fmt.Sprintf("\n%s: %s", string(msg.Who), msg.Content)))
}

func (cb *ChatBox) Widget() modal.InputCaptureable {
	return cb.widget
}

func (cb *ChatBox) Done() <-chan bool {
	return cb.exit
}
