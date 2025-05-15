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
	"github.com/google/oss-rebuild/tools/ctl/ide/assistant"
	"github.com/google/oss-rebuild/tools/ctl/ide/commands"
	"github.com/google/oss-rebuild/tools/ctl/ide/explorer"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/rivo/tview"
)

// TuiApp represents the entire IDE, containing UI widgets and worker processes.
type TuiApp struct {
	app       *tview.Application
	root      *tview.Pages
	explorer  *explorer.Explorer
	statusBox *tview.TextView
	logs      *tview.TextView
	benches   benchmark.Repository
	rb        *rebuilder.Rebuilder
}

// NewTuiApp creates a new tuiApp object.
func NewTuiApp(dex rundex.Reader, watcher rundex.Watcher, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, buildDefs rebuild.LocatableAssetStore, butler localfiles.Butler, asst assistant.Assistant) *TuiApp {
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
			app: app,
			// When the widgets are updated, we should refresh the application.
			statusBox: tview.NewTextView().SetChangedFunc(func() { app.Draw() }),
			logs:      logs,
			benches:   benches,
			rb:        rb,
			root:      tview.NewPages(),
		}
	}
	modalFn := func(input modal.InputCaptureable, opts modal.ModalOpts) func() {
		return modal.Show(t.app, t.root, input, opts)
	}
	cmdReg := commands.Registry{}
	if err := cmdReg.AddGlobals(commands.NewGlobalCmds(t.app, t.rb, modalFn, butler, asst, buildDefs, dex, benches)...); err != nil {
		log.Fatal(err)
	}
	if err := cmdReg.AddRebuildGroups(commands.NewRebuildGroupCmds(t.app, t.rb, modalFn, butler, asst, buildDefs, dex, benches)...); err != nil {
		log.Fatal(err)
	}
	if err := cmdReg.AddRebuilds(commands.NewRebuildCmds(t.app, t.rb, modalFn, butler, asst, buildDefs, dex, benches)...); err != nil {
		log.Fatal(err)
	}
	err := cmdReg.AddGlobals([]commands.GlobalCmd{
		{
			Short:  "logs up",
			Hotkey: '^',
			Func: func(_ context.Context) {
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
			Short:  "logs bottom",
			Hotkey: 'v',
			Func: func(_ context.Context) {
				t.logs.ScrollToEnd()
			},
		},
		{
			Short:  "refresh",
			Hotkey: 'f',
			Func: func(ctx context.Context) {
				if err := t.explorer.LoadTree(ctx); err != nil {
					log.Println(err)
					return
				}
			},
		},
	}...)
	if err != nil {
		log.Fatal(err)
	}
	t.explorer = explorer.NewExplorer(t.app, modalFn, dex, watcher, rundexOpts, benches, cmdReg)
	gcmds := cmdReg.GlobalCommands()
	inst := make([]string, 0, len(gcmds))
	for _, cmd := range gcmds {
		inst = append(inst, fmt.Sprintf("%c: %s", cmd.Hotkey, cmd.Short))
	}
	instructions := tview.NewTextView().SetText(strings.Join(inst, " "))
	{
		/*             window
		┌───────────────────────────────────┐
		│┼─────────────────────────────────┼│
		││               .                 ││
		││               .                 ││
		││          ◄-mainPane-►           ││
		││               .                 ││
		││               .                 ││
		││    explorer   .      logs       ││
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
			AddItem(instructions, flexed, unit, !focused). // instr
			AddItem(t.statusBox, flexed, unit, !focused)   // status
		mainPane := tview.NewFlex().SetDirection(tview.FlexColumn).
			AddItem(t.explorer.Container(), flexed, unit, focused). // explorer
			AddItem(t.logs, flexed, unit, !focused)                 // logs
		window := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(mainPane, flexed, unit, focused).
			AddItem(bottomBar, 1, 0, !focused) // bottomBar is non-flexed, fixed height 1
		window.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			for _, cmd := range gcmds {
				if cmd.Hotkey != 0 && event.Rune() == cmd.Hotkey {
					go cmd.Func(context.Background())
					return nil
				}
			}
			return event
		})
		t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyCtrlC {
				// Clean up the rebuilder docker container.
				t.rb.Kill()
				return event
			}
			return event
		})
		t.root.AddPage("main window", window, true, true)
	}
	t.app.SetRoot(t.root, true)
	return t
}

// Run runs the underlying tview app.
func (t *TuiApp) Run(ctx context.Context) error {
	go func() {
		if err := t.explorer.LoadTree(ctx); err != nil {
			log.Println(err)
			return
		}
		t.app.Draw()
		log.Println("Finished loading the tree.")
	}()
	return t.app.Run()
}
