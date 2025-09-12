// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundextree

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

type Tree struct {
	*tview.TreeView
	app        *tview.Application
	root       *tview.TreeNode
	dex        rundex.Reader
	rundexOpts rundex.FetchRebuildOpts
	runs       map[string]rundex.Run
	benches    benchmark.Repository
	cmdReg     *commandreg.Registry
	modalFn    modal.Fn
}

func New(app *tview.Application, modalFn modal.Fn, dex rundex.Reader, rundexOpts rundex.FetchRebuildOpts, benches benchmark.Repository, cmdReg *commandreg.Registry) *Tree {
	root := tview.NewTreeNode("root").SetColor(tcell.ColorRed)
	t := &Tree{
		TreeView:   tview.NewTreeView().SetRoot(root).SetCurrentNode(root),
		app:        app,
		root:       root,
		dex:        dex,
		rundexOpts: rundexOpts,
		benches:    benches,
		cmdReg:     cmdReg,
		modalFn:    modalFn,
	}
	return t
}

// LoadTree will query rundex for all the runs, then display them.
func (t *Tree) LoadTree(ctx context.Context) error {
	t.root.ClearChildren()
	log.Printf("Fetching runs...")
	runs, err := t.dex.FetchRuns(ctx, rundex.FetchRunsOpts{})
	if err != nil {
		return err
	}
	log.Printf("Found %d runs", len(runs))
	byBench := make(map[string][]string)
	t.runs = make(map[string]rundex.Run)
	for _, run := range runs {
		byBench[run.BenchmarkName] = append(byBench[run.BenchmarkName], run.ID)
		t.runs[run.ID] = run
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
		t.root.AddChild(t.makeRunGroupNode(benchName, byBench[benchName]))
	}
	return nil
}

// LoadRebuilds will group and display the provided rebuilds.
func (t *Tree) LoadRebuilds(rebuilds []rundex.Rebuild) {
	t.root.ClearChildren()
	byCount := rundex.GroupRebuilds(rebuilds)
	var agg float32
	for i := len(byCount) - 1; i >= 0; i-- {
		pct := 100 * float32(byCount[i].Count) / float32(len(rebuilds))
		agg += pct
		vgnode := t.makeVerdictGroupNode(byCount[i], pct, agg)
		t.root.AddChild(vgnode)
	}
}

func (t *Tree) commandNodes(example rundex.Rebuild) []*tview.TreeNode {
	var res []*tview.TreeNode
	for _, cmd := range t.cmdReg.RebuildCommands() {
		if cmd.Func == nil {
			continue
		}
		if cmd.IsDisabled() {
			res = append(res, tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorGrey).SetSelectedFunc(func() { go t.modalFn(tview.NewTextView().SetText(cmd.DisabledMsg()), modal.ModalOpts{Margin: 10}) }))
		} else {
			res = append(res, tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), example) }))
		}
	}
	return res
}

type NodeData struct {
	NodeID   string
	Rebuilds []*rundex.Rebuild
}

func (t *Tree) makeExampleNode(example rundex.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow)
	node.SetReference(&NodeData{NodeID: example.ID(), Rebuilds: []*rundex.Rebuild{&example}})
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, c := range t.commandNodes(example) {
				node.AddChild(c)
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (t *Tree) makeVerdictGroupNode(vg *rundex.VerdictGroup, percent, cumulative float32) *tview.TreeNode {
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
	node := tview.NewTreeNode(fmt.Sprintf("%4d %s %s %s", vg.Count, pct, fmt.Sprintf("%3.0f%%", cumulative), msg)).SetColor(tcell.ColorGreen).SetSelectable(true).SetReference(vg)
	data := &NodeData{NodeID: vg.Msg, Rebuilds: nil}
	for _, r := range vg.Examples {
		data.Rebuilds = append(data.Rebuilds, &r)
	}
	node.SetReference(data)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, cmd := range t.cmdReg.RebuildGroupCommands() {
				if cmd.IsDisabled() {
					node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorGrey).SetSelectedFunc(func() { go t.modalFn(tview.NewTextView().SetText(cmd.DisabledMsg()), modal.ModalOpts{Margin: 10}) }))
				} else {
					node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), vg.Examples) }))
				}
			}
			for _, example := range vg.Examples {
				node.AddChild(t.makeExampleNode(example))
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (t *Tree) makeRunNode(runid string) *tview.TreeNode {
	var title string
	if run, ok := t.runs[runid]; ok && run.Type == schema.AttestMode {
		title = fmt.Sprintf("%s (publish)", runid)
	} else if run, ok := t.runs[runid]; ok && run.Type == schema.SmoketestMode {
		title = fmt.Sprintf("%s (evaluate)", runid)
	} else {
		title = fmt.Sprintf("%s (unknown)", runid)
	}
	node := tview.NewTreeNode(title).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetReference(&NodeData{NodeID: runid, Rebuilds: nil})
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			log.Printf("Fetching rebuilds...")
			rebuilds, err := t.dex.FetchRebuilds(context.Background(), &rundex.FetchRebuildRequest{Runs: []string{runid}, Opts: t.rundexOpts, LatestPerPackage: true})
			if err != nil {
				log.Println(errors.Wrapf(err, "failed to get rebuilds for runid: %s", runid))
				return
			}
			log.Printf("Fetched %d rebuilds", len(rebuilds))
			data, ok := node.GetReference().(*NodeData)
			if !ok {
				log.Println("Node missing reference")
				return
			} else {
				for _, r := range rebuilds {
					data.Rebuilds = append(data.Rebuilds, &r)
				}
			}
			byCount := rundex.GroupRebuilds(rebuilds)
			var agg float32
			for i := len(byCount) - 1; i >= 0; i-- {
				pct := 100 * float32(byCount[i].Count) / float32(len(rebuilds))
				agg += pct
				vgnode := t.makeVerdictGroupNode(byCount[i], pct, agg)
				node.AddChild(vgnode)
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (t *Tree) makeRunGroupNode(benchName string, runs []string) *tview.TreeNode {
	node := tview.NewTreeNode(fmt.Sprintf("%3d %s", len(runs), benchName)).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetReference(&NodeData{NodeID: benchName, Rebuilds: nil})
	for _, cmd := range t.cmdReg.BenchmarkCommands() {
		if cmd.IsDisabled() {
			node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorGrey).SetSelectedFunc(func() { go t.modalFn(tview.NewTextView().SetText(cmd.DisabledMsg()), modal.ModalOpts{Margin: 10}) }))
		} else {
			node.AddChild(tview.NewTreeNode(cmd.Short).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), benchName) }))
		}
	}
	for _, run := range runs {
		node.AddChild(t.makeRunNode(run))
	}
	node.SetExpanded(false)
	node.SetSelectedFunc(func() {
		node.SetExpanded(!node.IsExpanded())
	})
	return node
}

func find(root *tview.TreeNode, nodeID string) (node *tview.TreeNode) {
	children := root.GetChildren()
	for _, child := range children {
		data, ok := child.GetReference().(*NodeData)
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
		data, ok := child.GetReference().(*NodeData)
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

func (t *Tree) HandleRunUpdate(r *rundex.Run) error {
	t.runs[r.ID] = *r
	rg, added := findOrAdd(t.root, r.BenchmarkName, func() *tview.TreeNode {
		return t.makeRunGroupNode(r.BenchmarkName, []string{r.ID})
	})
	if added {
		return nil
	}
	findOrAdd(rg, r.ID, func() *tview.TreeNode {
		return t.makeRunNode(r.ID)
	})
	// TODO: Update the run group's number of runs.
	return nil
}

func (t *Tree) HandleRebuildUpdate(r *rundex.Rebuild) error {
	run, ok := t.runs[r.RunID]
	if !ok {
		return errors.Errorf("rebuild update from unknown run %s", r.RunID)
	}
	rg := find(t.root, run.BenchmarkName)
	if rg == nil {
		return errors.Errorf("Unable to find rungroup for  %s", run.BenchmarkName)
	}
	runNode := find(rg, run.ID)
	if runNode == nil {
		return errors.Errorf("Unable to find run node for  %s", run.ID)
	}
	runNodeData, ok := runNode.GetReference().(*NodeData)
	if !ok {
		return errors.Errorf("Run node missing reference")
	}
	runNodeData.Rebuilds = append(runNodeData.Rebuilds, r)
	if len(runNode.GetChildren()) == 0 {
		return nil
	}
	// TODO: Update the verdict group's stats (percent, count)
	vg, added := findOrAdd(runNode, r.Message, func() *tview.TreeNode {
		return t.makeVerdictGroupNode(&rundex.VerdictGroup{Msg: r.Message, Count: 1, Examples: nil}, 100*float32(1)/float32(len(runNodeData.Rebuilds)), 0.0)
	})
	vgData, ok := vg.GetReference().(*NodeData)
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
		return t.makeExampleNode(*r)
	})
	return nil
}
