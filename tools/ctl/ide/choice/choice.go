// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package choice

import (
	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/rivo/tview"
)

const (
	defaultBackground = tcell.ColorDarkCyan
)

// Choice provides a UI element and return channel for selecting from a list of options.
func Choice(all []string) (modal.InputCaptureable, modal.ModalOpts, <-chan string) {
	options := tview.NewList()
	options.SetBackgroundColor(defaultBackground).SetBorder(true).SetTitle("Select a benchmark to execute.")
	selected := make(chan string, 1)
	for _, option := range all {
		options.AddItem(option, "", 0, func() {
			selected <- option
			close(selected)
		})
	}
	return options, modal.ModalOpts{Height: (options.GetItemCount() * 2) + 2, Margin: 10}, selected
}
