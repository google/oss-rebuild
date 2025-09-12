// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundextable

import (
	"context"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/rivo/tview"
)

func verdictAsEmoji(r rundex.Rebuild) string {
	if r.Success || r.Message == "" {
		return "✅"
	} else {
		return "❌"
	}
}

func addHeader(table *tview.Table, headers []string) {
	for i, h := range headers {
		table.SetCell(0, i, tview.NewTableCell(h).SetTextColor(tcell.ColorYellow).SetSelectable(false))
	}
	table.SetFixed(1, 0)
}

func addRow(table *tview.Table, row int, elems []string) {
	for i, e := range elems {
		table.SetCellSimple(row, i, e)
	}
}

func New(rebuilds []rundex.Rebuild, cmdReg *commandreg.Registry, onSelect func(rebuild rundex.Rebuild)) (*tview.Table, error) {
	table := tview.NewTable().SetBorders(true)
	addHeader(table, []string{"ID", "Success", "Run"})
	for i, r := range rebuilds {
		addRow(table, i+1, []string{r.ID(), verdictAsEmoji(r), r.RunID})
	}
	if len(rebuilds) > 0 {
		table.Select(1, 0)
	}
	table.ScrollToBeginning()
	table.SetSelectable(true, false)
	table.SetSelectedFunc(func(row int, column int) {
		if onSelect != nil {
			onSelect(rebuilds[row-1])
		}
	})
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		row, _ := table.GetSelection()
		if row == 0 || row > len(rebuilds) {
			return event
		}
		for _, cmd := range cmdReg.RebuildCommands() {
			if cmd.Func == nil || cmd.Hotkey == 0 || cmd.IsDisabled() {
				continue
			}
			if event.Rune() == cmd.Hotkey {
				go cmd.Func(context.Background(), rebuilds[row-1])
				return nil
			}
		}
		return event
	})
	return table, nil
}
