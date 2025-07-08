// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuildhistory

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	detailsui "github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/rivo/tview"
)

const (
	defaultBackground = tcell.ColorDarkGray
)

func verdictAsEmoji(r rundex.Rebuild) string {
	if r.Success || r.Message == "" {
		return "✅"
	} else {
		return "❌"
	}
}

// New creates a new RebuildHistory viewer.
func New(modalFn modal.Fn, cmdReg commandreg.Registry, rebuilds []rundex.Rebuild) modal.InputCaptureable {
	slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
		return -strings.Compare(a.RunID, b.RunID)
	})
	details := tview.NewTextView()
	runs := tview.NewTreeView()
	root := tview.NewTreeNode("runs").SetColor(tcell.ColorRed)
	runs.SetRoot(root)
	for i := range rebuilds {
		r := rebuilds[i]
		node := tview.NewTreeNode(r.RunID + verdictAsEmoji(r)).SetReference(&r)
		node.SetSelectedFunc(func() {
			children := node.GetChildren()
			if len(children) == 0 {
				node.SetExpanded(true)
				for _, cmd := range cmdReg.RebuildCommands() {
					if cmd.Func == nil {
						continue
					}
					if cmd.IsDisabled() {
						node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorGrey).SetSelectedFunc(func() { go modalFn(tview.NewTextView().SetText(cmd.DisabledMsg()), modal.ModalOpts{Margin: 10}) }))
					} else {
						node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), r) }))
					}
				}
			} else {
				node.SetExpanded(!node.IsExpanded())
			}
			if node.IsExpanded() {
				for _, c := range root.GetChildren() {
					if c != node && c.IsExpanded() {
						c.SetExpanded(false)
					}
				}
			}
		})
		root.AddChild(node)
	}
	runs.SetBackgroundColor(defaultBackground)
	runs.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		node := runs.GetCurrentNode()
		if node == nil {
			return event
		}
		rebuild, ok := node.GetReference().(*rundex.Rebuild)
		if !ok {
			return event
		}
		for _, cmd := range cmdReg.RebuildCommands() {
			if cmd.Func == nil || cmd.Hotkey == 0 || cmd.IsDisabled() {
				continue
			}
			if event.Rune() == cmd.Hotkey {
				go cmd.Func(context.Background(), *rebuild)
				return nil
			}
		}
		return event
	})
	populateDetails := func(node *tview.TreeNode) {
		if node == root {
			details.SetText("")
			return
		}
		d, ok := node.GetReference().(*rundex.Rebuild)
		if !ok {
			log.Println("Node has unexpected reference")
			details.SetText("ERROR: Node has unexpected reference")
			return
		}
		text, err := detailsui.Format(*d)
		if err != nil {
			log.Println(err)
			details.SetText(fmt.Sprintf("ERROR: %v", err))
			return
		}
		details.SetText(text)
	}
	runs.SetChangedFunc(populateDetails)
	if len(rebuilds) > 0 {
		if first := root.GetChildren()[0]; first != nil {
			runs.SetCurrentNode(first)
			populateDetails(first)
		}
	}
	return tview.NewFlex().SetDirection(tview.FlexColumn).AddItem(runs, 25, 0, true).AddItem(details, 0, 1, false)
}
