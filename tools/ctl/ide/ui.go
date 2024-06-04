// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package ide contains UI and state management code for the TUI rebuild debugger.
package ide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tcell "github.com/gdamore/tcell/v2"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/ctl/firestore"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	yaml "gopkg.in/yaml.v3"
)

// Returns a new primitive which puts the provided primitive in the center and
// sets its size to the given width and height.
func modal(p tview.Primitive, margin int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, margin, 0, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, margin, 0, false).
			AddItem(p, 0, 1, true).
			AddItem(nil, margin, 0, false), 0, 1, true).
		AddItem(nil, margin, 0, false)
}

// The explorer is the Tree structure on the left side of the TUI
type explorer struct {
	ctx           context.Context
	app           *tview.Application
	container     *tview.Pages
	tree          *tview.TreeView
	root          *tview.TreeNode
	rb            *Rebuilder
	firestore     *firestore.Client
	firestoreOpts firestore.FetchRebuildOpts
}

func newExplorer(ctx context.Context, app *tview.Application, firestore *firestore.Client, firestoreOpts firestore.FetchRebuildOpts, rb *Rebuilder) *explorer {
	e := explorer{
		ctx:           ctx,
		app:           app,
		container:     tview.NewPages(),
		tree:          tview.NewTreeView(),
		root:          tview.NewTreeNode("root").SetColor(tcell.ColorRed),
		rb:            rb,
		firestore:     firestore,
		firestoreOpts: firestoreOpts,
	}
	e.tree.SetRoot(e.root).SetCurrentNode(e.root)
	e.container.AddPage("explorer", e.tree, true, true)
	return &e
}

func makeCommandNode(name string, handler func()) *tview.TreeNode {
	return tview.NewTreeNode(name).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(handler)
}

// TODO: This was copied from npm, but this should be shared.
func sanitize(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "@", ""), "/", "-")
}

func localAssetStore(ctx context.Context, runID string) (rebuild.AssetStore, error) {
	// TODO: Maybe this should be a different ctx variable?
	dir := filepath.Join("/tmp/oss-rebuild", runID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create directory %s", dir)
	}
	assetsFS, err := osfs.New("/").Chroot(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
	}
	return rebuild.NewFilesystemAssetStore(assetsFS), nil
}

func gcsAssetStore(ctx context.Context, runID string) (rebuild.AssetStore, error) {
	bucket, ok := ctx.Value(rebuild.UploadArtifactsPathID).(string)
	if !ok {
		return nil, errors.Errorf("GCS bucket was not specified")
	}
	return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, runID), bucket)
}

func diffArtifacts(ctx context.Context, example firestore.Rebuild) {
	if example.Artifact == "" {
		log.Println("Firestore does not have the artifact, cannot find GCS path.")
		return
	}
	t := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(example.Ecosystem),
		Package:   example.Package,
		Version:   example.Version,
		Artifact:  example.Artifact,
	}
	localAssets, err := localAssetStore(ctx, example.Run)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create local asset store"))
		return
	}
	gcsAssets, err := gcsAssetStore(ctx, example.Run)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create gcs asset store"))
		return
	}
	// TODO: Clean up these artifacts.
	// TODO: Check if these are already downloaded.
	var rba, usa string
	rba, err = rebuild.AssetCopy(ctx, localAssets, gcsAssets, rebuild.Asset{Target: t, Type: rebuild.DebugRebuildAsset})
	if err != nil {
		log.Println(errors.Wrap(err, "failed to copy rebuild asset"))
		return
	}
	usa, err = rebuild.AssetCopy(ctx, localAssets, gcsAssets, rebuild.Asset{Target: t, Type: rebuild.DebugUpstreamAsset})
	if err != nil {
		log.Println(errors.Wrap(err, "failed to copy upstream asset"))
		return
	}
	log.Printf("downloaded rebuild and upstream:\n\t%s\n\t%s", rba, usa)
	cmd := exec.Command("tmux", "new-window", fmt.Sprintf("diffoscope --text-color=always %s %s | less -R", rba, usa))
	if err := cmd.Run(); err != nil {
		log.Println(errors.Wrap(err, "failed to run diffoscope"))
	}
}

func (e *explorer) showModal(ctx context.Context, tv *tview.TextView, onExit func()) {
	tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			e.container.RemovePage("modal")
			onExit()
		}
		return event
	})
	e.app.QueueUpdateDraw(func() {
		e.container.AddPage("modal", modal(tv, 10), true, true)
	})
}

func (e *explorer) showDetails(ctx context.Context, example firestore.Rebuild) {
	details := tview.NewTextView()

	var stratOneof schema.StrategyOneOf
	if err := json.Unmarshal([]byte(example.Strategy), &stratOneof); err != nil {
		log.Println(errors.Wrap(err, "failed to unmarshal strategy"))
		return
	}
	type detailsStruct struct {
		Success  bool
		Message  string
		Timings  rebuild.Timings
		Strategy schema.StrategyOneOf
	}
	detailsYaml := new(bytes.Buffer)
	enc := yaml.NewEncoder(detailsYaml)
	enc.SetIndent(2)
	err := enc.Encode(detailsStruct{
		Success:  example.Success,
		Message:  example.Message,
		Timings:  example.Timings,
		Strategy: stratOneof,
	})
	if err != nil {
		log.Println(errors.Wrap(err, "failed to marshal details"))
		return
	}
	details.SetText(detailsYaml.String()).SetTitle("Execution details").SetBackgroundColor(tcell.ColorDarkCyan)
	e.showModal(ctx, details, func() {})
}

func (e *explorer) showLogs(ctx context.Context, example firestore.Rebuild) {
	if example.Artifact == "" {
		log.Println("Firestore does not have the artifact, cannot find GCS path.")
		return
	}
	t := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(example.Ecosystem),
		Package:   example.Package,
		Version:   example.Version,
		Artifact:  example.Artifact,
	}
	localAssets, err := localAssetStore(ctx, example.Run)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create local asset store"))
		return
	}
	gcsAssets, err := gcsAssetStore(ctx, example.Run)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create gcs asset store"))
		return
	}
	logs, err := rebuild.AssetCopy(ctx, localAssets, gcsAssets, rebuild.Asset{Target: t, Type: rebuild.DebugLogsAsset})
	if err != nil {
		log.Println(errors.Wrap(err, "failed to copy rebuild asset"))
		return
	}
	cmd := exec.Command("tmux", "new-window", fmt.Sprintf("cat %s | less", logs))
	if err := cmd.Run(); err != nil {
		log.Println(errors.Wrap(err, "failed to read logs"))
	}
}

func (e *explorer) editAndRun(ctx context.Context, example firestore.Rebuild) {
	localAssets, err := localAssetStore(ctx, example.Run)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create local asset store"))
		return
	}
	buildDefAsset := rebuild.Asset{Type: rebuild.BuildDef, Target: example.Target()}
	var currentStrat schema.StrategyOneOf
	{
		if r, _, err := localAssets.Reader(ctx, buildDefAsset); err == nil {
			d := yaml.NewDecoder(r)
			if d.Decode(&currentStrat) != nil {
				log.Println(errors.Wrap(err, "failed to read existing build definition"))
				return
			}
		} else {
			if err := json.Unmarshal([]byte(example.Strategy), &currentStrat); err != nil {
				log.Println(errors.Wrap(err, "failed to parse strategy"))
				return
			}
		}
	}
	var newStrat schema.StrategyOneOf
	{
		w, uri, err := localAssets.Writer(ctx, buildDefAsset)
		if err != nil {
			log.Println(errors.Wrapf(err, "opening strategy file"))
			return
		}
		if _, err = w.Write([]byte("# Edit the strategy below, then save and exit the file to begin a rebuild.\n")); err != nil {
			log.Println(errors.Wrapf(err, "writing comment to strategy file"))
			return
		}
		e := yaml.NewEncoder(w)
		if e.Encode(&currentStrat) != nil {
			log.Println(errors.Wrapf(err, "populating strategy file"))
			return
		}
		w.Close()
		// Send a "tmux wait -S" signal once the edit is complete.
		cmd := exec.Command("tmux", "new-window", fmt.Sprintf("$EDITOR %s; tmux wait -S editing", uri))
		if out, err := cmd.Output(); err != nil {
			log.Println(errors.Wrap(err, "failed to edit strategy"))
			log.Println(out)
			return
		}
		// Wait to receive the tmux signal.
		if _, err := exec.Command("tmux", "wait", "editing").Output(); err != nil {
			log.Println(errors.Wrap(err, "failed to wait for tmux signal"))
			return
		}
		r, _, err := localAssets.Reader(ctx, buildDefAsset)
		if err != nil {
			log.Println(errors.Wrap(err, "failed to open strategy after edits"))
			return
		}
		d := yaml.NewDecoder(r)
		if err := d.Decode(&newStrat); err != nil {
			log.Println(errors.Wrap(err, "manual strategy failed to parse"))
			return
		}
	}
	newStratJsonBytes, err := json.Marshal(newStrat)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to convert new strategy to json"))
		return
	}
	go e.rb.RunLocal(e.ctx, example, "strategy="+url.QueryEscape(string(newStratJsonBytes)))
}

func (e *explorer) makeExampleNode(example firestore.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			node.AddChild(makeCommandNode("run local", func() {
				go e.rb.RunLocal(e.ctx, example)
			}))
			node.AddChild(makeCommandNode("restart && run local", func() {
				go func() {
					e.rb.Restart(e.ctx)
					e.rb.RunLocal(e.ctx, example)
				}()
			}))
			node.AddChild(makeCommandNode("edit and run local", func() {
				go e.editAndRun(e.ctx, example)
			}))
			node.AddChild(makeCommandNode("details", func() {
				go e.showDetails(e.ctx, example)
			}))
			node.AddChild(makeCommandNode("logs", func() {
				go e.showLogs(e.ctx, example)
			}))
			node.AddChild(makeCommandNode("diff", func() {
				go diffArtifacts(e.ctx, example)
			}))
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

func (e *explorer) makeVerdictGroupNode(vg *firestore.VerdictGroup, percent float32) *tview.TreeNode {
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

func (e *explorer) makeRunNode(runid string) *tview.TreeNode {
	node := tview.NewTreeNode(runid).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			rebuilds, err := e.firestore.FetchRebuilds(e.ctx, &firestore.FetchRebuildRequest{Runs: []string{runid}, Opts: e.firestoreOpts})
			if err != nil {
				log.Println(errors.Wrapf(err, "failed to get rebuilds for runid: %s", runid))
				return
			}
			byCount := firestore.GroupRebuilds(rebuilds)
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

func (e *explorer) makeRunGroupNode(benchName string, runs []string) *tview.TreeNode {
	node := tview.NewTreeNode(fmt.Sprintf("%3d %s", len(runs), benchName)).SetColor(tcell.ColorGreen).SetSelectable(true)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			for _, run := range runs {
				node.AddChild(e.makeRunNode(run))
			}
		} else {
			node.SetExpanded(!node.IsExpanded())
		}
	})
	return node
}

// LoadTree will query firestore for all the runs, then display them.
func (e *explorer) LoadTree() error {
	e.root.ClearChildren()
	runs, err := e.firestore.FetchRuns(e.ctx, firestore.FetchRunsOpts{})
	if err != nil {
		return err
	}
	byBench := make(map[string][]string)
	for _, run := range runs {
		if run.Type == firestore.AttestMode {
			continue
		}
		byBench[run.BenchmarkName] = append(byBench[run.BenchmarkName], run.ID)
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

type tuiAppCmd struct {
	Name string
	Rune rune
	Func func()
}

// TuiApp represents the entire IDE, containing UI widgets and worker processes.
type TuiApp struct {
	Ctx       context.Context
	app       *tview.Application
	explorer  *explorer
	statusBox *tview.TextView
	logs      *tview.TextView
	cmds      []tuiAppCmd
	rb        *Rebuilder
}

// NewTuiApp creates a new tuiApp object.
func NewTuiApp(ctx context.Context, fireClient *firestore.Client, firestoreOpts firestore.FetchRebuildOpts) *TuiApp {
	var t *TuiApp
	{
		app := tview.NewApplication()
		// Capture logs as early as possible
		logs := tview.NewTextView().SetChangedFunc(func() { app.Draw() })
		// TODO: Also log to stdout, because currently a panic/fatal message is silent.
		log.Default().SetOutput(logs)
		log.Default().SetPrefix(logPrefix("ctl"))
		log.Default().SetFlags(0)
		logs.SetBorder(true).SetTitle("Logs")
		rb := &Rebuilder{}
		t = &TuiApp{
			Ctx:      ctx,
			app:      app,
			explorer: newExplorer(ctx, app, fireClient, firestoreOpts, rb),
			// When the widgets are updated, we should refresh the application.
			statusBox: tview.NewTextView().SetChangedFunc(func() { app.Draw() }),
			logs:      logs,
			rb:        rb,
		}
	}
	t.cmds = []tuiAppCmd{
		{
			Name: "restart rebuilder",
			Rune: 'r',
			Func: func() { t.rb.Restart(t.Ctx) },
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
				if err := t.rb.Attach(t.Ctx); err != nil {
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
	}

	var root tview.Primitive
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
			AddItem(t.explorer.container, flexed, unit, focused). // tree
			AddItem(t.logs, flexed, unit, !focused)               // logs
		window := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(mainPane, flexed, unit, focused).
			AddItem(bottomBar, unit, 0, !focused)
		root = window
	}
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
