// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package explorer

import (
	"context"
	"log"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuildhistory"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextable"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextree"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
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
	cmdReg     commandreg.Registry
	modalFn    modal.Fn
}

func NewExplorer(app *tview.Application, modalFn modal.Fn, dex rundex.Reader, watcher rundex.Watcher, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, cmdReg commandreg.Registry) *Explorer {
	e := Explorer{
		app:        app,
		container:  tview.NewPages(),
		dex:        dex,
		watcher:    watcher,
		rundexOpts: rundexOpts,
		benches:    benches,
		cmdReg:     cmdReg,
		modalFn:    modalFn,
	}
	err := e.cmdReg.AddBenchmarks(commandreg.BenchmarkCmd{
		Short: "View by target",
		Func: func(ctx context.Context, benchName string) {
			all, err := e.benches.List()
			if err != nil {
				log.Println(err)
				return
			}
			var benchPath string
			for _, p := range all {
				if path.Base(p) == benchName {
					benchPath = p
					break
				}
			}
			if benchPath == "" {
				log.Printf("Benchmark %s not found", benchName)
				return
			}
			rebuilds, err := e.benchHistory(context.Background(), benchPath)
			if err != nil {
				log.Println(err)
				return
			}
			onSelect := func(rebuild rundex.Rebuild) {
				log.Println("Loading history for", rebuild.ID())
				t := rebuild.Target()
				rebuildsOfTarget, err := e.dex.FetchRebuilds(context.Background(), &rundex.FetchRebuildRequest{
					Target: &t,
					Opts:   e.rundexOpts,
				})
				if err != nil {
					log.Println(errors.Wrap(err, "fetching rebuilds for target"))
					return
				}
				e.modalFn(rebuildhistory.New(modalFn, cmdReg, rebuildsOfTarget), modal.ModalOpts{Margin: 10})
			}
			table, err := rundextable.New(rebuilds, cmdReg, onSelect)
			if err != nil {
				log.Println(err)
				return
			}
			e.modalFn(table, modal.ModalOpts{Margin: 10})
		},
	})
	if err != nil {
		log.Println("Adding benchmark command failed:", err)
	}
	e.tree = rundextree.New(app, modalFn, dex, rundexOpts, benches, e.cmdReg)
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

func (e *Explorer) benchHistory(ctx context.Context, benchPath string) ([]rundex.Rebuild, error) {
	tracked := make(map[string]bool)
	{
		set, err := e.benches.Load(benchPath)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark")
		}
		for _, p := range set.Packages {
			for i, v := range p.Versions {
				var a string
				if i < len(p.Artifacts) {
					a = p.Artifacts[i]
				}
				tracked[(&rundex.Rebuild{
					RebuildAttempt: schema.RebuildAttempt{
						Ecosystem: string(p.Ecosystem),
						Package:   p.Name,
						Version:   v,
						Artifact:  a,
					},
				}).ID()] = true
			}
		}
	}
	var rebuilds []rundex.Rebuild
	{
		log.Printf("Fetching rebuilds...")
		start := time.Now()
		var err error
		// TODO: Filter by runs that matched the benchmark instead.
		rebuilds, err = e.dex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{Opts: e.rundexOpts, LatestPerPackage: true})
		if err != nil {
			return nil, errors.Wrapf(err, "loading rebuilds")
		}
		log.Printf("Fetched %d rebuilds in %v", len(rebuilds), time.Since(start))
		slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
			return strings.Compare(a.ID(), b.ID())
		})
		rebuilds = slices.DeleteFunc(rebuilds, func(r rundex.Rebuild) bool {
			return !tracked[r.ID()]
		})
	}
	return rebuilds, nil
}

// LoadTree will query rundex for all the runs, then display them.
func (e *Explorer) LoadTree(ctx context.Context) error {
	return e.tree.LoadTree(ctx)
}

func (e *Explorer) SelectTree() {
	e.container.SwitchToPage(TreePageName)
}
