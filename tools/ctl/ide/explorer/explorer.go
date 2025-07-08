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
	detailsui "github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextree"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

const (
	defaultBackground = tcell.ColorGray
	TreePageName      = "treeView"
)

func verdictAsEmoji(r rundex.Rebuild) string {
	if r.Success || r.Message == "" {
		return "✅"
	} else {
		return "❌"
	}
}

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
			table, err := e.newPopulatedTable(rebuilds)
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

func (e *Explorer) commandNodes(example rundex.Rebuild) []*tview.TreeNode {
	var res []*tview.TreeNode
	for _, cmd := range e.cmdReg.RebuildCommands() {
		if cmd.Func == nil {
			continue
		}
		if cmd.IsDisabled() {
			res = append(res, tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorGrey).SetSelectedFunc(func() { go e.modalFn(tview.NewTextView().SetText(cmd.DisabledMsg()), modal.ModalOpts{Margin: 10}) }))
		} else {
			res = append(res, tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), example) }))
		}
	}
	return res
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

func (e *Explorer) rebuildHistory(rebuilds []rundex.Rebuild) (modal.InputCaptureable, error) {
	slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
		return -strings.Compare(a.RunID, b.RunID)
	})
	details := tview.NewTextView()
	runs := tview.NewTreeView()
	{
		root := tview.NewTreeNode("runs").SetColor(tcell.ColorRed)
		runs.SetRoot(root)
		for _, r := range rebuilds {
			node := tview.NewTreeNode(r.RunID + verdictAsEmoji(r)).SetReference(&r)
			node.SetSelectedFunc(func() {
				children := node.GetChildren()
				if len(children) == 0 {
					node.SetExpanded(true)
					for _, c := range e.commandNodes(r) {
						node.AddChild(c)
					}
				} else {
					node.SetExpanded(!node.IsExpanded())
				}
				// If we expanded this node, collapse the others.
				if node.IsExpanded() {
					for _, c := range root.GetChildren() {
						if c == node {
							continue
						}
						if c.IsExpanded() {
							c.SetExpanded(false)
						}
					}
				}
			})
			root.AddChild(node)
		}
		runs.SetBackgroundColor(defaultBackground)
		runs.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			data, ok := runs.GetCurrentNode().GetReference().(*rundex.Rebuild)
			if !ok {
				return event
			}
			return e.commandHotkeys(event, *data)
		})
		populateDetails := func(node *tview.TreeNode) {
			if node == root {
				details.SetText("")
				return
			}
			d, ok := node.GetReference().(*rundex.Rebuild)
			if !ok {
				log.Println("Node has unexpected reference")
				details.SetText("ERROR")
				return
			}
			text, err := detailsui.Format(*d)
			if err != nil {
				log.Println(err)
				details.SetText("ERROR")
				return
			}
			details.SetText(text)
		}
		runs.SetChangedFunc(populateDetails)
		if len(rebuilds) > 0 {
			first := root.GetChildren()[0]
			if first != nil {
				runs.SetCurrentNode(first)
				populateDetails(first)
			}
		}
	}
	history := tview.NewFlex().SetDirection(tview.FlexColumn).AddItem(runs, 25, 0, true).AddItem(details, 0, 1, false)
	return history, nil
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

func (e *Explorer) newPopulatedTable(rebuilds []rundex.Rebuild) (*tview.Table, error) {
	table := tview.NewTable().SetBorders(true)
	addHeader(table, []string{"ID", "Success", "Run"})
	for i, r := range rebuilds {
		addRow(table, i+1, []string{r.ID(), verdictAsEmoji(r), r.RunID})
	}
	// Configure selection behavior
	if len(rebuilds) > 0 {
		table.Select(1, 0)
	}
	table.ScrollToBeginning()
	table.SetSelectable(true, false)
	table.SetSelectedFunc(func(row int, column int) {
		r := rebuilds[row-1]
		// Load the rundex.Rebuilds for this particular target
		log.Println("Loading history for", r.ID())
		t := r.Target()
		rebuildsOfTarget, err := e.dex.FetchRebuilds(context.Background(), &rundex.FetchRebuildRequest{
			Target: &t,
			Opts:   e.rundexOpts,
		})
		if err != nil {
			log.Println(errors.Wrap(err, "fetching rebuilds for target"))
			return
		}
		// Build the UI
		hist, err := e.rebuildHistory(rebuildsOfTarget)
		if err != nil {
			log.Println(errors.Wrap(err, "browsing target's history"))
			return
		}
		go e.modalFn(hist, modal.ModalOpts{Margin: 10})
	})
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			e.SelectTree()
			// Return nil to stop further primatives from receiving the event.
			return nil
		}
		row, _ := table.GetSelection()
		if row == 0 || row > len(rebuilds) {
			return event
		}
		example := rebuilds[row-1]
		return e.commandHotkeys(event, example)
	})
	return table, nil
}
