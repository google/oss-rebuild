// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package explorer

import (
	"context"
	"fmt"
	"log"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/commands"
	detailsui "github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

const (
	defaultBackground = tcell.ColorGray
	TreePageName      = "treeView"
	TablePageName     = "tableView"
)

func verdictAsEmoji(r rundex.Rebuild) string {
	if r.Success || r.Message == "" {
		return "✅"
	} else {
		return "❌"
	}
}

// A modalFnType can be used to show an InputCaptureable. It returns an exit function that can be used to close the modal.
type modalFnType func(modal.InputCaptureable, modal.ModalOpts) func()

// The Explorer is the Tree structure on the left side of the TUI
type Explorer struct {
	app        *tview.Application
	container  *tview.Pages
	table      *tview.Table
	tree       *tview.TreeView
	root       *tview.TreeNode
	dex        rundex.Reader
	rundexOpts rundex.FetchRebuildOpts
	runs       map[string]rundex.Run
	benches    benchmark.Repository
	cmdReg     commands.Registry
	modalFn    modalFnType
}

func NewExplorer(app *tview.Application, modalFn modalFnType, dex rundex.Reader, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, cmdReg commands.Registry) *Explorer {
	e := Explorer{
		app:        app,
		container:  tview.NewPages(),
		table:      tview.NewTable().SetBorders(true),
		tree:       tview.NewTreeView(),
		root:       tview.NewTreeNode("root").SetColor(tcell.ColorRed),
		dex:        dex,
		rundexOpts: rundexOpts,
		benches:    benches,
		cmdReg:     cmdReg,
		modalFn:    modalFn,
	}
	e.tree.SetRoot(e.root).SetCurrentNode(e.root)
	e.tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		current := e.tree.GetCurrentNode().GetReference()
		example, ok := current.(*rundex.Rebuild)
		if !ok || example == nil {
			return event
		}
		return e.commandHotkeys(event, *example)
	})
	resize, show := true, true
	e.container.AddPage(TablePageName, e.table, resize, !show)
	e.container.AddPage(TreePageName, e.tree, resize, show)
	e.SelectTree()
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
		res = append(res, tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), example) }))
	}
	return res
}

func (e *Explorer) commandHotkeys(event *tcell.EventKey, example rundex.Rebuild) *tcell.EventKey {
	for _, cmd := range e.cmdReg.RebuildCommands() {
		if cmd.Func == nil || cmd.Hotkey == 0 {
			continue
		}
		if event.Rune() == cmd.Hotkey {
			go cmd.Func(context.Background(), example)
			return nil
		}
	}
	return event
}

func (e *Explorer) makeExampleNode(example rundex.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow).SetReference(&example)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, c := range e.commandNodes(example) {
				node.AddChild(c)
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (e *Explorer) makeVerdictGroupNode(vg *rundex.VerdictGroup, percent float32) *tview.TreeNode {
	var msg string
	if vg.Msg == "" {
		msg = "Success!"
	} else {
		msg = vg.Msg
	}
	var pct string
	if percent < 1. {
		pct = fmt.Sprintf(" <1%%")
	} else {
		pct = fmt.Sprintf("%3.0f%%", percent)
	}
	node := tview.NewTreeNode(fmt.Sprintf("%4d %s %s", vg.Count, pct, msg)).SetColor(tcell.ColorGreen).SetSelectable(true).SetReference(vg)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, example := range vg.Examples {
				node.AddChild(e.makeExampleNode(example))
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (e *Explorer) makeRunNode(runid string) *tview.TreeNode {
	var title string
	if run, ok := e.runs[runid]; ok && run.Type == schema.AttestMode {
		title = fmt.Sprintf("%s (publish)", runid)
	} else if run, ok := e.runs[runid]; ok && run.Type == schema.SmoketestMode {
		title = fmt.Sprintf("%s (evaluate)", runid)
	} else {
		title = fmt.Sprintf("%s (unknown)", runid)
	}
	node := tview.NewTreeNode(title).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			log.Printf("Fetching rebuilds...")
			rebuilds, err := e.dex.FetchRebuilds(context.Background(), &rundex.FetchRebuildRequest{Runs: []string{runid}, Opts: e.rundexOpts, LatestPerPackage: true})
			if err != nil {
				log.Println(errors.Wrapf(err, "failed to get rebuilds for runid: %s", runid))
				return
			}
			log.Printf("Fetched %d rebuilds", len(rebuilds))
			byCount := rundex.GroupRebuilds(rebuilds)
			for i := len(byCount) - 1; i >= 0; i-- {
				vgnode := e.makeVerdictGroupNode(byCount[i], 100*float32(byCount[i].Count)/float32(len(rebuilds)))
				node.AddChild(vgnode)
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
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

func (e *Explorer) makeRunGroupNode(benchName string, runs []string) *tview.TreeNode {
	node := tview.NewTreeNode(fmt.Sprintf("%3d %s", len(runs), benchName)).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			node.AddChild(tview.NewTreeNode("View by target").SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() {
				go func() {
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
					e.app.QueueUpdateDraw(func() {
						if err := e.populateTable(rebuilds); err != nil {
							log.Println(err)
						}
						e.SelectTable()
					})
				}()
			}))
			for _, run := range runs {
				node.AddChild(e.makeRunNode(run))
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

// LoadTree will query rundex for all the runs, then display them.
func (e *Explorer) LoadTree(ctx context.Context) error {
	e.root.ClearChildren()
	log.Printf("Fetching runs...")
	runs, err := e.dex.FetchRuns(ctx, rundex.FetchRunsOpts{})
	if err != nil {
		return err
	}
	log.Printf("Found %d runs", len(runs))
	byBench := make(map[string][]string)
	e.runs = make(map[string]rundex.Run)
	for _, run := range runs {
		byBench[run.BenchmarkName] = append(byBench[run.BenchmarkName], run.ID)
		e.runs[run.ID] = run
	}
	sortedBenchNames := make([]string, 0, len(byBench))
	for benchName := range byBench {
		sortedBenchNames = append(sortedBenchNames, benchName)
		// Also sort the order of runs.
		slices.Sort(byBench[benchName])
		// Reverse to make sure recent is at the top.
		slices.Reverse(byBench[benchName])
	}
	sort.Strings(sortedBenchNames)
	for _, benchName := range sortedBenchNames {
		e.root.AddChild(e.makeRunGroupNode(benchName, byBench[benchName]))
	}
	return nil
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
			current := runs.GetCurrentNode().GetReference()
			example, ok := current.(*rundex.Rebuild)
			if !ok || example == nil {
				return event
			}
			return e.commandHotkeys(event, *example)
		})
		populateDetails := func(node *tview.TreeNode) {
			if node == root {
				details.SetText("")
				return
			}
			r, ok := node.GetReference().(*rundex.Rebuild)
			if !ok {
				log.Println("Node missing rebuild reference")
				return
			}
			text, err := detailsui.Format(*r)
			if err != nil {
				log.Println(err)
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

func (e *Explorer) populateTable(rebuilds []rundex.Rebuild) error {
	e.table.Clear()
	addHeader(e.table, []string{"ID", "Success", "Run"})
	for i, r := range rebuilds {
		addRow(e.table, i+1, []string{r.ID(), verdictAsEmoji(r), r.RunID})
	}
	// Configure selection behavior
	if len(rebuilds) > 0 {
		e.table.Select(1, 0)
	}
	e.table.ScrollToBeginning()
	e.table.SetSelectable(true, false)
	e.table.SetSelectedFunc(func(row int, column int) {
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
	e.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			e.SelectTree()
			// Return nil to stop further primatives from receiving the event.
			return nil
		}
		row, _ := e.table.GetSelection()
		if row == 0 || row > len(rebuilds) {
			return event
		}
		example := rebuilds[row-1]
		return e.commandHotkeys(event, example)
	})
	return nil
}

func (e *Explorer) SelectTable() {
	e.container.SwitchToPage(TablePageName)
}
