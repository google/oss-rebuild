// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package textinput

import (
	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/rivo/tview"
)

type TextInputOpts struct {
	Header string
}

func TextInput(opts TextInputOpts) (modal.InputCaptureable, modal.ModalOpts, <-chan string) {
	ta := tview.NewTextArea()
	output := make(chan string, 1)
	ta.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			input := ta.GetText()
			output <- input
			return nil
		}
		return event
	})
	// TODO: Is there a better way to set focus on the pattern input without a flex box?
	flx := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ta, 0, 1, true)
	flx.SetBorder(true)
	if opts.Header != "" {
		flx.SetTitle(opts.Header)
	}
	return flx, modal.ModalOpts{Width: 50, Height: 3}, output
}
