// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package ide contains UI and state management code for the TUI rebuild debugger.
package ide

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/explorer"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

const (
	defaultBackground = tcell.ColorDarkCyan
)

type tuiAppCmd struct {
	Name string
	Rune rune
	Func func()
}

// TuiApp represents the entire IDE, containing UI widgets and worker processes.
type TuiApp struct {
	ctx       context.Context
	app       *tview.Application
	root      *tview.Pages
	explorer  *explorer.Explorer
	statusBox *tview.TextView
	logs      *tview.TextView
	cmds      []tuiAppCmd
	benches   benchmark.Repository
	rb        *rebuilder.Rebuilder
}

// NewTuiApp creates a new tuiApp object.
func NewTuiApp(ctx context.Context, dex rundex.Reader, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, buildDefs rebuild.LocatableAssetStore, butler localfiles.Butler) *TuiApp {
	var t *TuiApp
	{
		app := tview.NewApplication()
		// Capture logs as early as possible
		logs := tview.NewTextView().SetChangedFunc(func() { app.Draw() })
		// TODO: Also log to stdout, because currently a panic/fatal message is silent.
		log.Default().SetOutput(logs)
		log.Default().SetPrefix(fmt.Sprintf("[%-9s]", "ctl"))
		log.Default().SetFlags(0)
		logs.SetBorder(true).SetTitle("Logs")
		logs.ScrollToEnd()
		rb := &rebuilder.Rebuilder{}
		t = &TuiApp{
			ctx:      ctx,
			app:      app,
			explorer: explorer.NewExplorer(ctx, app, dex, rundexOpts, rb, buildDefs, butler, benches),
			// When the widgets are updated, we should refresh the application.
			statusBox: tview.NewTextView().SetChangedFunc(func() { app.Draw() }),
			logs:      logs,
			benches:   benches,
			rb:        rb,
		}
	}
	t.cmds = []tuiAppCmd{
		{
			Name: "restart rebuilder",
			Rune: 'r',
			Func: func() { t.rb.Restart(t.ctx) },
		},
		{
			Name: "kill rebuilder",
			Rune: 'x',
			Func: func() {
				t.rb.Kill()
			},
		},
		{
			Name: "attach",
			Rune: 'a',
			Func: func() {
				if err := t.rb.Attach(t.ctx); err != nil {
					log.Println(err)
				}
				t.updateStatus()
			},
		},
		{
			Name: "logs up",
			Rune: '^',
			Func: func() {
				curRow, _ := t.logs.GetScrollOffset()
				_, _, _, height := t.logs.GetInnerRect()
				newRow := curRow - (height - 5)
				if newRow > 0 {
					t.logs.ScrollTo(newRow, 0)
				} else {
					t.logs.ScrollTo(0, 0)
				}
			},
		},
		{
			Name: "logs bottom",
			Rune: 'v',
			Func: func() {
				t.logs.ScrollToEnd()
			},
		},
		{
			Name: "benchmark",
			Rune: 'b',
			Func: func() {
				if b, err := t.promptForBenchmark(); err != nil {
					t.modalText(err.Error())
				} else {
					go func() {
						t.explorer.RunBenchmark(<-b)
					}()
				}
			},
		},
	}

	var root *tview.Pages
	{
		/*             window
		┌───────────────────────────────────┐
		│┼─────────────────────────────────┼│
		││               .                 ││
		││               .                 ││
		││          ◄-mainPane-►           ││
		││               .                 ││
		││               .                 ││
		││    tree       .      logs       ││
		││               .                 ││
		││               .                 ││
		│┼─────────────────────────────────┼│
		├───────────────────────────────────┤
		│  instr   ◄-bottomBar-►    status  │
		└───────────────────────────────────┘
		*/
		flexed := 0
		unit := 1
		focused := true
		bottomBar := tview.NewFlex().SetDirection(tview.FlexColumn).
			AddItem(t.instructions(), flexed, unit, !focused). // instr
			AddItem(t.statusBox, flexed, unit, !focused)       // status
		mainPane := tview.NewFlex().SetDirection(tview.FlexColumn).
			AddItem(t.explorer.Container(), flexed, unit, focused). // tree
			AddItem(t.logs, flexed, unit, !focused)                 // logs
		window := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(mainPane, flexed, unit, focused).
			AddItem(bottomBar, unit, 0, !focused)
		container := tview.NewPages().AddPage("main window", window, true, true)
		root = container
	}
	t.root = root
	t.app.SetRoot(root, true).SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			// Clean up the rebuilder docker container.
			t.rb.Kill()
			return event
		}
		for _, cmd := range t.cmds {
			if event.Rune() == cmd.Rune {
				go cmd.Func()
				break
			}
		}
		return event
	})
	return t
}

func (t *TuiApp) instructions() *tview.TextView {
	inst := make([]string, 0, len(t.cmds))
	for _, cmd := range t.cmds {
		inst = append(inst, fmt.Sprintf("%c: %s", cmd.Rune, cmd.Name))
	}
	return tview.NewTextView().SetText(strings.Join(inst, " "))
}

func (t *TuiApp) updateStatus() {
	cid := "N/A"
	if inst := t.rb.Instance(); inst.Serving() {
		cid = string(inst.ID)
	}
	t.statusBox.SetText(fmt.Sprintf("rebuilder cid: %s", cid))
}

func (t *TuiApp) modalText(content string) {
	modal.Text(t.app, t.root, content)
}

func (t *TuiApp) promptForBenchmark() (<-chan string, error) {
	all, err := t.benches.List()
	if err != nil {
		return nil, errors.Wrap(err, "listing benchmarks")
	}
	options := tview.NewList()
	options.SetBackgroundColor(defaultBackground).SetBorder(true).SetTitle("Select a benchmark to execute.")
	// exitFunc will be populated once the modal has been created.
	var exitFunc func()
	selected := make(chan string, 1)
	for _, path := range all {
		options.AddItem(path, "", 0, func() {
			if exitFunc != nil {
				exitFunc()
			}
			selected <- path
		})
	}
	exitFunc = modal.Show(t.app, t.root, options, modal.ModalOpts{Height: (options.GetItemCount() * 2) + 2, Margin: 10})
	return selected, nil
}

// Run runs the underlying tview app.
func (t *TuiApp) Run() error {
	go func() {
		if err := t.explorer.LoadTree(); err != nil {
			log.Println(err)
			return
		}
		t.app.Draw()
		log.Println("Finished loading the tree.")
	}()
	return t.app.Run()
}
