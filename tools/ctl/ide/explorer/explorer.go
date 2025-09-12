// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package explorer

import (
	"context"
	"log"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextree"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/rivo/tview"
)

const (
	TreePageName = "treeView"
)

// The Explorer is the Tree structure on the left side of the TUI
type Explorer struct {
	app        *tview.Application
	container  *tview.Pages
	tree       *rundextree.Tree
	dex        rundex.Reader
	watcher    rundex.Watcher
	rundexOpts rundex.FetchRebuildOpts
	benches    benchmark.Repository
	cmdReg     *commandreg.Registry
	modalFn    modal.Fn
}

func NewExplorer(app *tview.Application, modalFn modal.Fn, dex rundex.Reader, watcher rundex.Watcher, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, cmdReg *commandreg.Registry) *Explorer {
	e := Explorer{
		app:        app,
		container:  tview.NewPages(),
		tree:       rundextree.New(app, modalFn, dex, rundexOpts, benches, cmdReg),
		dex:        dex,
		watcher:    watcher,
		rundexOpts: rundexOpts,
		benches:    benches,
		cmdReg:     cmdReg,
		modalFn:    modalFn,
	}
	e.tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		data, ok := e.tree.GetCurrentNode().GetReference().(*rundextree.NodeData)
		if !ok || len(data.Rebuilds) != 1 {
			return event
		}
		return e.commandHotkeys(event, *data.Rebuilds[0])
	})
	resize, show := true, true
	e.container.AddPage(TreePageName, e.tree, resize, show)
	e.SelectTree()
	if e.watcher != nil {
		rebuildNotify := e.watcher.WatchRebuilds()
		go func() {
			for r := range rebuildNotify {
				app.QueueUpdateDraw(func() {
					if err := e.tree.HandleRebuildUpdate(r); err != nil {
						log.Println(err)
					}
				})
			}
		}()
		runNotify := e.watcher.WatchRuns()
		go func() {
			for r := range runNotify {
				app.QueueUpdateDraw(func() {
					if err := e.tree.HandleRunUpdate(r); err != nil {
						log.Println(err)
					}
				})
			}
		}()
	} else {
		log.Println("Failed to register watcher, TUI will not dynamically refresh")
	}
	return &e
}

func (e *Explorer) Container() tview.Primitive {
	return e.container
}

func (e *Explorer) commandHotkeys(event *tcell.EventKey, example rundex.Rebuild) *tcell.EventKey {
	for _, cmd := range e.cmdReg.RebuildCommands() {
		if cmd.Func == nil || cmd.Hotkey == 0 || cmd.IsDisabled() {
			continue
		}
		if event.Rune() == cmd.Hotkey {
			go cmd.Func(context.Background(), example)
			return nil
		}
	}
	return event
}

// LoadTree will query rundex for all the runs, then display them.
func (e *Explorer) LoadTree(ctx context.Context) error {
	return e.tree.LoadTree(ctx)
}

func (e *Explorer) SelectTree() {
	e.container.SwitchToPage(TreePageName)
}
