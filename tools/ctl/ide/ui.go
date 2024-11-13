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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tcell "github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	yaml "gopkg.in/yaml.v3"
)

const (
	defaultModalBackground = tcell.ColorDarkCyan
)

// Returns a new primitive which puts the provided primitive in the center and
// adds vertical and horizontal margin.
func modal(p tview.Primitive, vertMargin, horizMargin int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, horizMargin, 0, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, vertMargin, 0, false).
			AddItem(p, 0, 1, true).
			AddItem(nil, vertMargin, 0, false), 0, 1, true).
		AddItem(nil, horizMargin, 0, false)
}

type inputCaptureable interface {
	tview.Primitive
	SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey) *tview.Box
}

type modalOpts struct {
	Height int
	Width  int
	Margin int
}

func showModal(app *tview.Application, container *tview.Pages, contents inputCaptureable, opts modalOpts) (exitFunc func()) {
	pageName := fmt.Sprintf("modal%d", container.GetPageCount()+1)
	exitFunc = func() {
		container.RemovePage(pageName)
	}
	contents.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			exitFunc()
		}
		return event
	})
	_, _, containerWidth, containerHeight := container.GetInnerRect()
	// If opts.Width or opts.Height is zero, assume the full container size.
	if opts.Width == 0 {
		opts.Width = containerWidth
	}
	if opts.Height == 0 {
		opts.Height = containerHeight
	}
	// Always apply the margin (default is zero).
	opts.Height = min(opts.Height, containerHeight-(2*opts.Margin))
	opts.Width = min(opts.Width, containerWidth-(2*opts.Margin))
	app.QueueUpdateDraw(func() {
		container.AddPage(pageName, modal(contents, (containerHeight-opts.Height)/2, (containerWidth-opts.Width)/2), true, true)
	})
	return exitFunc
}

// The explorer is the Tree structure on the left side of the TUI
type explorer struct {
	ctx           context.Context
	app           *tview.Application
	container     *tview.Pages
	tree          *tview.TreeView
	root          *tview.TreeNode
	rb            *Rebuilder
	firestore     rundex.Reader
	firestoreOpts rundex.FetchRebuildOpts
}

func newExplorer(ctx context.Context, app *tview.Application, firestore rundex.Reader, firestoreOpts rundex.FetchRebuildOpts, rb *Rebuilder) *explorer {
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

func diffArtifacts(ctx context.Context, example rundex.Rebuild) {
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
	localAssets, err := localfiles.AssetStore(example.RunID)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create local asset store"))
		return
	}
	debugAssets, err := rebuild.DebugStoreFromContext(context.WithValue(ctx, rebuild.RunID, example.RunID))
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create debug asset store"))
		return
	}
	// TODO: Clean up these artifacts.
	// TODO: Check if these are already downloaded.
	rebuildAsset := rebuild.Asset{Target: t, Type: rebuild.DebugRebuildAsset}
	upstreamAsset := rebuild.Asset{Target: t, Type: rebuild.DebugUpstreamAsset}
	rba := localAssets.URL(rebuildAsset).Path
	usa := localAssets.URL(upstreamAsset).Path
	if _, err := os.Stat(rba); errors.Is(err, os.ErrNotExist) {
		if err := rebuild.AssetCopy(ctx, localAssets, debugAssets, rebuildAsset); err != nil {
			log.Println(errors.Wrap(err, "failed to copy rebuild asset"))
			return
		}
	}
	if _, err := os.Stat(usa); errors.Is(err, os.ErrNotExist) {
		if err := rebuild.AssetCopy(ctx, localAssets, debugAssets, upstreamAsset); err != nil {
			log.Println(errors.Wrap(err, "failed to copy upstream asset"))
			return
		}
		log.Printf("downloaded rebuild and upstream:\n\t%s\n\t%s", rba, usa)
	}
	cmd := exec.Command("tmux", "new-window", fmt.Sprintf("diffoscope --text-color=always %s %s | less -R", rba, usa))
	if err := cmd.Run(); err != nil {
		log.Println(errors.Wrap(err, "failed to run diffoscope"))
		if err.Error() == "exit status 1" {
			log.Println("Maybe you're not running inside a tmux session?")
		}
	}
}

func (e *explorer) showDetails(ctx context.Context, example rundex.Rebuild) {
	details := tview.NewTextView()
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
		Strategy: example.Strategy,
	})
	if err != nil {
		log.Println(errors.Wrap(err, "failed to marshal details"))
		return
	}
	details.SetText(detailsYaml.String()).SetBackgroundColor(defaultModalBackground).SetTitle("Execution details")
	showModal(e.app, e.container, details, modalOpts{Margin: 10})
}

func (e *explorer) showLogs(ctx context.Context, example rundex.Rebuild) {
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
	localAssets, err := localfiles.AssetStore(example.RunID)
	if err != nil {
		log.Println(errors.Wrap(err, "failed to create local asset store"))
		return
	}
	logsAsset := rebuild.Asset{Target: t, Type: rebuild.DebugLogsAsset}
	logs := localAssets.URL(logsAsset).Path
	if _, err := os.Stat(logs); errors.Is(err, os.ErrNotExist) {
		debugAssets, err := rebuild.DebugStoreFromContext(context.WithValue(ctx, rebuild.RunID, example.RunID))
		if err != nil {
			log.Println(errors.Wrap(err, "failed to create debug asset store"))
			return
		}
		if err := rebuild.AssetCopy(ctx, localAssets, debugAssets, logsAsset); err != nil {
			log.Println(errors.Wrap(err, "failed to copy logs asset"))
			return
		}
	}
	cmd := exec.Command("tmux", "new-window", fmt.Sprintf("cat %s | less", logs))
	if err := cmd.Run(); err != nil {
		log.Println(errors.Wrap(err, "failed to read logs"))
		if err.Error() == "exit status 1" {
			log.Println("Maybe you're not running inside a tmux session?")
		}
	}
}

func (e *explorer) editAndRun(ctx context.Context, example rundex.Rebuild) error {
	localAssets, err := localfiles.AssetStore(example.RunID)
	if err != nil {
		return errors.Wrap(err, "failed to create local asset store")
	}
	buildDefAsset := rebuild.Asset{Type: rebuild.BuildDef, Target: example.Target()}
	var currentStrat schema.StrategyOneOf
	{
		if r, err := localAssets.Reader(ctx, buildDefAsset); err == nil {
			d := yaml.NewDecoder(r)
			if d.Decode(&currentStrat) != nil {
				return errors.Wrap(err, "failed to read existing build definition")
			}
		} else {
			currentStrat = example.Strategy
		}
	}
	var newStrat schema.StrategyOneOf
	{
		w, err := localAssets.Writer(ctx, buildDefAsset)
		if err != nil {
			return errors.Wrapf(err, "opening build definition")
		}
		if _, err = w.Write([]byte("# Edit the build definition below, then save and exit the file to begin a rebuild.\n")); err != nil {
			return errors.Wrapf(err, "writing comment to build definition file")
		}
		e := yaml.NewEncoder(w)
		if e.Encode(&currentStrat) != nil {
			return errors.Wrapf(err, "populating build definition")
		}
		w.Close()
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vim"
		}
		// Send a "tmux wait -S" signal once the edit is complete.
		cmd := exec.Command("tmux", "new-window", fmt.Sprintf("%s %s; tmux wait -S editing", editor, localAssets.URL(buildDefAsset).Path))
		if _, err := cmd.Output(); err != nil {
			return errors.Wrap(err, "failed to edit build definition")
		}
		// Wait to receive the tmux signal.
		if _, err := exec.Command("tmux", "wait", "editing").Output(); err != nil {
			return errors.Wrap(err, "failed to wait for tmux signal")
		}
		r, err := localAssets.Reader(ctx, buildDefAsset)
		if err != nil {
			return errors.Wrap(err, "failed to open build definition after edits")
		}
		d := yaml.NewDecoder(r)
		if err := d.Decode(&newStrat); err != nil {
			return errors.Wrap(err, "manual strategy oneof failed to parse")
		}
	}
	e.rb.RunLocal(e.ctx, example, RunLocalOpts{Strategy: &newStrat})
	return nil
}

func (e *explorer) makeExampleNode(example rundex.Rebuild) *tview.TreeNode {
	name := fmt.Sprintf("%s [%ds]", example.ID(), int(example.Timings.EstimateCleanBuild().Seconds()))
	node := tview.NewTreeNode(name).SetColor(tcell.ColorYellow)
	node.SetSelectedFunc(func() {
		children := node.GetChildren()
		if len(children) == 0 {
			node.AddChild(makeCommandNode("run local", func() {
				go e.rb.RunLocal(e.ctx, example, RunLocalOpts{})
			}))
			node.AddChild(makeCommandNode("restart && run local", func() {
				go func() {
					e.rb.Restart(e.ctx)
					e.rb.RunLocal(e.ctx, example, RunLocalOpts{})
				}()
			}))
			node.AddChild(makeCommandNode("edit and run local", func() {
				go func() {
					if err := e.editAndRun(e.ctx, example); err != nil {
						log.Println(err.Error())
					}
				}()
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

func (e *explorer) makeVerdictGroupNode(vg *rundex.VerdictGroup, percent float32) *tview.TreeNode {
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
			rebuilds, err := e.firestore.FetchRebuilds(e.ctx, &rundex.FetchRebuildRequest{Runs: []string{runid}, Opts: e.firestoreOpts})
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
	runs, err := e.firestore.FetchRuns(e.ctx, rundex.FetchRunsOpts{})
	if err != nil {
		return err
	}
	byBench := make(map[string][]string)
	for _, run := range runs {
		if run.Type == benchmark.AttestMode {
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
	Ctx          context.Context
	app          *tview.Application
	root         *tview.Pages
	explorer     *explorer
	statusBox    *tview.TextView
	logs         *tview.TextView
	cmds         []tuiAppCmd
	benchmarkDir string
	rb           *Rebuilder
}

// NewTuiApp creates a new tuiApp object.
func NewTuiApp(ctx context.Context, fireClient rundex.Reader, firestoreOpts rundex.FetchRebuildOpts, benchmarkDir string) *TuiApp {
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
		logs.ScrollToEnd()
		rb := &Rebuilder{}
		t = &TuiApp{
			Ctx:      ctx,
			app:      app,
			explorer: newExplorer(ctx, app, fireClient, firestoreOpts, rb),
			// When the widgets are updated, we should refresh the application.
			statusBox:    tview.NewTextView().SetChangedFunc(func() { app.Draw() }),
			logs:         logs,
			benchmarkDir: benchmarkDir,
			rb:           rb,
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
		{
			Name: "benchmark",
			Rune: 'b',
			Func: func() {
				t.selectBenchmark()
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
			AddItem(t.explorer.container, flexed, unit, focused). // tree
			AddItem(t.logs, flexed, unit, !focused)               // logs
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
	tv := tview.NewTextView()
	tv.SetText("\n" + content + "\n").
		SetTextAlign(tview.AlignCenter).
		SetTextColor(tcell.ColorWhite).
		SetBackgroundColor(defaultModalBackground)
	showModal(t.app, t.root, tv, modalOpts{Height: 3, Margin: 10})
}

func (t *TuiApp) runBenchmark(bench string) {
	fire, ok := t.explorer.firestore.(rundex.Writer)
	if !ok {
		log.Println("Cannot run benchmark with non-local firestore client.")
		return
	}
	set, err := benchmark.ReadBenchmark(bench)
	if err != nil {
		log.Println(errors.Wrap(err, "reading benchmark"))
		return
	}
	runID := time.Now().UTC().Format(time.RFC3339)
	fire.WriteRun(t.Ctx, rundex.Run{
		ID:            runID,
		BenchmarkName: filepath.Base(bench),
		BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
		Type:          benchmark.SmoketestMode,
		Created:       time.Now(),
	})
	verdictChan, err := t.rb.RunBench(t.Ctx, set, runID)
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
		fire.WriteRebuild(t.Ctx, rundex.Rebuild{
			SmoketestAttempt: schema.SmoketestAttempt{
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

func (t *TuiApp) selectBenchmark() {
	if t.benchmarkDir == "" {
		t.modalText("No benchmark dir provided.")
		return
	}
	options := tview.NewList()
	options.SetBackgroundColor(defaultModalBackground).SetBorder(true).SetTitle("Select a benchmark to execute.")
	// exitFunc will be populated once the modal has been created.
	var exitFunc func()
	err := filepath.Walk(t.benchmarkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Best effort reading, skip failures.
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		options.AddItem(name, "", 0, func() {
			go t.runBenchmark(path)
			if exitFunc != nil {
				exitFunc()
			}
		})
		return nil
	})
	if err != nil {
		t.modalText(errors.Wrap(err, "walking benchmark dir").Error())
		return
	}
	exitFunc = showModal(t.app, t.root, options, modalOpts{Height: (options.GetItemCount() * 2) + 2, Margin: 10})
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
