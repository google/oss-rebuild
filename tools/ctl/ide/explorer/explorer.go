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
	watcher    rundex.Watcher
	rundexOpts rundex.FetchRebuildOpts
	runs       map[string]rundex.Run
	benches    benchmark.Repository
	cmdReg     commands.Registry
	modalFn    modalFnType
}

func NewExplorer(app *tview.Application, modalFn modalFnType, dex rundex.Reader, watcher rundex.Watcher, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, cmdReg commands.Registry) *Explorer {
	e := Explorer{
		app:        app,
		container:  tview.NewPages(),
		table:      tview.NewTable().SetBorders(true),
		tree:       tview.NewTreeView(),
		root:       tview.NewTreeNode("root").SetColor(tcell.ColorRed),
		dex:        dex,
		watcher:    watcher,
		rundexOpts: rundexOpts,
		benches:    benches,
		cmdReg:     cmdReg,
		modalFn:    modalFn,
	}
	e.tree.SetRoot(e.root).SetCurrentNode(e.root)
	e.tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		data, ok := e.tree.GetCurrentNode().GetReference().(*nodeData)
		if !ok || len(data.Rebuilds) != 1 {
			return event
		}
		return e.commandHotkeys(event, *data.Rebuilds[0])
	})
	resize, show := true, true
	e.container.AddPage(TablePageName, e.table, resize, !show)
	e.container.AddPage(TreePageName, e.tree, resize, show)
	e.SelectTree()
	if e.watcher != nil {
		rebuildNotify := e.watcher.WatchRebuilds()
		go func() {
			for r := range rebuildNotify {
				app.QueueUpdateDraw(func() {
					if err := e.handleRebuildUpdate(r); err != nil {
						log.Println(err)
					}
				})
			}
		}()
		runNotify := e.watcher.WatchRuns()
		go func() {
			for r := range runNotify {
				app.QueueUpdateDraw(func() {
					if err := e.handleRunUpdate(r); err != nil {
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

type nodeData struct {
	NodeID   string
	Rebuilds []*rundex.Rebuild
}

func (e *Explorer) makeExampleNode(example rundex.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow)
	node.SetReference(&nodeData{NodeID: example.ID(), Rebuilds: []*rundex.Rebuild{&example}})
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
	data := &nodeData{NodeID: vg.Msg, Rebuilds: nil}
	for _, r := range vg.Examples {
		data.Rebuilds = append(data.Rebuilds, &r)
	}
	node.SetReference(data)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, cmd := range e.cmdReg.RebuildGroupCommands() {
				node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), vg.Examples) }))
			}
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
	node.SetReference(&nodeData{NodeID: runid, Rebuilds: nil})
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
			data, ok := node.GetReference().(*nodeData)
			if !ok {
				log.Println("Node missing reference")
				return
			} else {
				for _, r := range rebuilds {
					data.Rebuilds = append(data.Rebuilds, &r)
				}
			}
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
	node.SetReference(&nodeData{NodeID: benchName, Rebuilds: nil})
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
	node.SetExpanded(false)
	node.SetSelectedFunc(func() {
		node.SetExpanded(!node.IsExpanded())
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

func find(root *tview.TreeNode, nodeID string) (node *tview.TreeNode) {
	children := root.GetChildren()
	for _, child := range children {
		data, ok := child.GetReference().(*nodeData)
		if !ok {
			continue
		}
		if data.NodeID == nodeID {
			return child
		}
	}
	return nil
}

func findOrAdd(root *tview.TreeNode, nodeID string, nodeFn func() *tview.TreeNode) (node *tview.TreeNode, added bool) {
	target := find(root, nodeID)
	if target != nil {
		return target, false
	}
	children := root.GetChildren()
	node = nodeFn()
	for i, child := range children {
		data, ok := child.GetReference().(*nodeData)
		if !ok {
			continue
		}
		if nodeID > data.NodeID {
			children = slices.Insert(children, i, node)
			root.SetChildren(children)
			return node, true
		}
	}
	children = append(children, node)
	root.SetChildren(children)
	return node, true
}

func (e *Explorer) handleRunUpdate(r *rundex.Run) error {
	e.runs[r.ID] = *r
	rg, added := findOrAdd(e.root, r.BenchmarkName, func() *tview.TreeNode {
		return e.makeRunGroupNode(r.BenchmarkName, []string{r.ID})
	})
	if added {
		return nil
	}
	findOrAdd(rg, r.ID, func() *tview.TreeNode {
		return e.makeRunNode(r.ID)
	})
	// TODO: Update the run group's number of runs.
	return nil
}

func (e *Explorer) handleRebuildUpdate(r *rundex.Rebuild) error {
	run, ok := e.runs[r.RunID]
	if !ok {
		return errors.Errorf("rebuild update from unknown run %s", r.RunID)
	}
	rg := find(e.root, run.BenchmarkName)
	if rg == nil {
		return errors.Errorf("Unable to find rungroup for  %s", run.BenchmarkName)
	}
	runNode := find(rg, run.ID)
	if runNode == nil {
		return errors.Errorf("Unable to find run node for  %s", run.ID)
	}
	runNodeData, ok := runNode.GetReference().(*nodeData)
	if !ok {
		return errors.Errorf("Run node missing reference")
	}
	runNodeData.Rebuilds = append(runNodeData.Rebuilds, r)
	if len(runNode.GetChildren()) == 0 {
		return nil
	}
	// TODO: Update the verdict group's stats (percent, count)
	vg, added := findOrAdd(runNode, r.Message, func() *tview.TreeNode {
		return e.makeVerdictGroupNode(&rundex.VerdictGroup{Msg: r.Message, Count: 1, Examples: nil}, 100*float32(1)/float32(len(runNodeData.Rebuilds)))
	})
	vgData, ok := vg.GetReference().(*nodeData)
	if !ok {
		return errors.Errorf("Verdict group node missing reference")
	}
	vgData.Rebuilds = append(vgData.Rebuilds, r)
	if len(vg.GetChildren()) == 0 {
		// TOOD: Update the other verdict group's percentages
		return nil
	}
	if added {
		return nil
	}
	findOrAdd(vg, r.ID(), func() *tview.TreeNode {
		return e.makeExampleNode(*r)
	})
	return nil
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
			data, ok := runs.GetCurrentNode().GetReference().(nodeData)
			if !ok || len(data.Rebuilds) != 1 {
				return event
			}
			return e.commandHotkeys(event, *data.Rebuilds[0])
		})
		populateDetails := func(node *tview.TreeNode) {
			if node == root {
				details.SetText("")
				return
			}
			d, ok := node.GetReference().(nodeData)
			if !ok || len(d.Rebuilds) != 1 {
				log.Println("Node has unexpected number of associated rebuilds: ", len(d.Rebuilds))
				details.SetText("ERROR")
				return
			}
			text, err := detailsui.Format(*d.Rebuilds[0])
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
