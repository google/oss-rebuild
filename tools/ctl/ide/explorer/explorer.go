// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package explorer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/ide/tmux"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
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

// rebuildCmd is a command that operates on an individual rundex.Rebuild
// These commands will be called as goroutines to avoid blocking the TUI loop
type rebuildCmd struct {
	Name string
	Func func(context.Context, rundex.Rebuild)
}

// The Explorer is the Tree structure on the left side of the TUI
type Explorer struct {
	app             *tview.Application
	container       *tview.Pages
	table           *tview.Table
	tree            *tview.TreeView
	root            *tview.TreeNode
	rb              *rebuilder.Rebuilder
	dex             rundex.Reader
	rundexOpts      rundex.FetchRebuildOpts
	runs            map[string]rundex.Run
	buildDefs       rebuild.LocatableAssetStore
	butler          localfiles.Butler
	benches         benchmark.Repository
	exampleCommands []rebuildCmd
}

func NewExplorer(app *tview.Application, dex rundex.Reader, rundexOpts rundex.FetchRebuildOpts, rb *rebuilder.Rebuilder, buildDefs rebuild.LocatableAssetStore, butler localfiles.Butler, benches benchmark.Repository) *Explorer {
	e := Explorer{
		app:             app,
		container:       tview.NewPages(),
		table:           tview.NewTable().SetBorders(true),
		tree:            tview.NewTreeView(),
		root:            tview.NewTreeNode("root").SetColor(tcell.ColorRed),
		rb:              rb,
		dex:             dex,
		rundexOpts:      rundexOpts,
		buildDefs:       buildDefs,
		butler:          butler,
		benches:         benches,
		exampleCommands: nil,
	}
	e.tree.SetRoot(e.root).SetCurrentNode(e.root)
	e.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			e.SelectTree()
			// Return nil to stop further primatives from receiving the event.
			return nil
		}
		return event
	})
	e.exampleCommands = []rebuildCmd{
		{
			Name: "run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				e.rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Name: "restart && run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				e.rb.Restart(ctx)
				e.rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Name: "edit and run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if err := e.editAndRun(ctx, example); err != nil {
					log.Println(err.Error())
				}
			},
		},
		{
			Name: "details",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if details, err := makeDetails(example); err != nil {
					log.Println(err.Error())
					return
				} else {
					modal.Show(e.app, e.container, details, modal.ModalOpts{Margin: 10})
				}
			},
		},
		{
			Name: "logs",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				e.showLogs(ctx, example)
			},
		},
		{
			Name: "diff",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				path, err := e.butler.Fetch(ctx, example.RunID, example.WasSmoketest(), diffoscope.DiffAsset.For(example.Target()))
				if err != nil {
					log.Println(errors.Wrap(err, "fetching diff"))
					return
				}
				if err := tmux.Wait(fmt.Sprintf("less -R %s", path)); err != nil {
					log.Println(errors.Wrap(err, "running diffoscope"))
					return
				}
			},
		},
	}
	resize, show := true, true
	e.container.AddPage(TablePageName, e.table, resize, !show)
	e.container.AddPage(TreePageName, e.tree, resize, show)
	e.SelectTree()
	return &e
}

func (e *Explorer) Container() tview.Primitive {
	return e.container
}

func (e *Explorer) modalText(content string) {
	modal.Text(e.app, e.container, content)
}

func makeCommandNode(name string, handler func()) *tview.TreeNode {
	return tview.NewTreeNode(name).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(handler)
}

func makeDetailsString(example rundex.Rebuild) (string, error) {
	type deets struct {
		Success  bool
		Message  string
		Timings  rebuild.Timings
		Strategy schema.StrategyOneOf
	}
	detailsYaml := new(bytes.Buffer)
	enc := yaml.NewEncoder(detailsYaml)
	enc.SetIndent(2)
	err := enc.Encode(deets{
		Success:  example.Success,
		Message:  example.Message,
		Timings:  example.Timings,
		Strategy: example.Strategy,
	})
	if err != nil {
		return "", errors.Wrap(err, "marshalling details")
	}
	return detailsYaml.String(), nil
}

func makeDetails(example rundex.Rebuild) (modal.InputCaptureable, error) {
	details := tview.NewTextView()
	text, err := makeDetailsString(example)
	if err != nil {
		return nil, err
	}
	details.SetText(text).SetBackgroundColor(defaultBackground).SetTitle("Execution details")
	return details, nil
}

func (e *Explorer) showLogs(ctx context.Context, example rundex.Rebuild) {
	if example.Artifact == "" {
		log.Println("Rundex does not have the artifact, cannot find GCS path.")
		return
	}
	logs, err := e.butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugLogsAsset.For(example.Target()))
	if err != nil {
		log.Println(errors.Wrap(err, "downloading logs"))
		return
	}
	if err := tmux.Start(fmt.Sprintf("cat %s | less", logs)); err != nil {
		log.Println(errors.Wrap(err, "failed to read logs"))
	}
}

func (e *Explorer) editAndRun(ctx context.Context, example rundex.Rebuild) error {
	buildDefAsset := rebuild.BuildDef.For(example.Target())
	var currentStrat schema.StrategyOneOf
	{
		if r, err := e.buildDefs.Reader(ctx, buildDefAsset); err == nil {
			d := yaml.NewDecoder(r)
			if d.Decode(&currentStrat) != nil {
				return errors.Wrap(err, "failed to read existing build definition")
			}
		} else {
			currentStrat = example.Strategy
			s, err := currentStrat.Strategy()
			if err != nil {
				return errors.Wrap(err, "unpacking StrategyOneOf")
			}
			// Convert this strategy to a workflow strategy if possible.
			if fable, ok := s.(rebuild.Flowable); ok {
				currentStrat = schema.NewStrategyOneOf(fable.ToWorkflow())
			}
		}
	}
	var newStrat schema.StrategyOneOf
	{
		w, err := e.buildDefs.Writer(ctx, buildDefAsset)
		if err != nil {
			return errors.Wrapf(err, "opening build definition")
		}
		if _, err = w.Write([]byte("# Edit the build definition below, then save and exit the file to begin a rebuild.\n")); err != nil {
			return errors.Wrapf(err, "writing comment to build definition file")
		}
		enc := yaml.NewEncoder(w)
		if enc.Encode(&currentStrat) != nil {
			return errors.Wrapf(err, "populating build definition")
		}
		w.Close()
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vim"
		}
		if err := tmux.Wait(fmt.Sprintf("%s %s", editor, e.buildDefs.URL(buildDefAsset).Path)); err != nil {
			return errors.Wrap(err, "editing build definition")
		}
		r, err := e.buildDefs.Reader(ctx, buildDefAsset)
		if err != nil {
			return errors.Wrap(err, "failed to open build definition after edits")
		}
		d := yaml.NewDecoder(r)
		if err := d.Decode(&newStrat); err != nil {
			return errors.Wrap(err, "manual strategy oneof failed to parse")
		}
	}
	e.rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{Strategy: &newStrat})
	return nil
}
func (e *Explorer) RunBenchmark(ctx context.Context, bench string) {
	wdex, ok := e.dex.(rundex.Writer)
	if !ok {
		log.Println("Cannot run benchmark with non-local rundex client.")
		return
	}
	set, err := benchmark.ReadBenchmark(bench)
	if err != nil {
		log.Println(errors.Wrap(err, "reading benchmark"))
		return
	}
	ts := time.Now().UTC()
	runID := ts.Format(time.RFC3339)
	wdex.WriteRun(ctx, rundex.FromRun(schema.Run{
		ID:            runID,
		BenchmarkName: filepath.Base(bench),
		BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
		Type:          string(schema.SmoketestMode),
		Created:       ts.UnixMilli(),
	}))
	verdictChan, err := e.rb.RunBench(ctx, set, runID)
	if err != nil {
		log.Println(err.Error())
		return
	}
	var successes int
	for v := range verdictChan {
		if v.Message == "" {
			successes += 1
		}
		now := time.Now().UnixMilli()
		wdex.WriteRebuild(ctx, rundex.Rebuild{
			RebuildAttempt: schema.RebuildAttempt{
				Ecosystem:       string(v.Target.Ecosystem),
				Package:         v.Target.Package,
				Version:         v.Target.Version,
				Artifact:        v.Target.Artifact,
				Success:         v.Message == "",
				Message:         v.Message,
				Strategy:        v.StrategyOneof,
				Timings:         v.Timings,
				ExecutorVersion: "local",
				RunID:           runID,
				Created:         now,
			},
			Created: time.UnixMilli(now),
		})
	}
	log.Printf("Finished benchmark %s with %d successes.", bench, successes)
}

func (e *Explorer) makeExampleNode(example rundex.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, cmd := range e.exampleCommands {
				node.AddChild(
					tview.NewTreeNode(cmd.Name).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), example) }),
				)
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
			node.AddChild(makeCommandNode("View by target", func() {
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
							e.modalText(err.Error())
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
			node := tview.NewTreeNode(r.RunID + verdictAsEmoji(r)).SetReference(r)
			node.SetSelectedFunc(func() {
				children := node.GetChildren()
				if len(children) == 0 {
					node.SetExpanded(true)
					for _, cmd := range e.exampleCommands {
						node.AddChild(
							tview.NewTreeNode(cmd.Name).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(func() { go cmd.Func(context.Background(), r) }),
						)
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
		populateDetails := func(node *tview.TreeNode) {
			if node == root {
				details.SetText("")
				return
			}
			r, ok := node.GetReference().(rundex.Rebuild)
			if !ok {
				log.Println("Node missing rebuild reference")
				return
			}
			text, err := makeDetailsString(r)
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
		go modal.Show(e.app, e.container, hist, modal.ModalOpts{Margin: 10})
	})
	return nil
}

func (e *Explorer) SelectTable() {
	e.container.SwitchToPage(TablePageName)
}
