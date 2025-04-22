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
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

func tmuxWait(cmd string) error {
	// Send a "tmux wait -S" signal once the cmd is complete.
	done := fmt.Sprintf("done%d", time.Now().UnixNano())
	c := exec.Command("tmux", "new-window", fmt.Sprintf("%s; tmux wait -S %s", cmd, done))
	if _, err := c.Output(); err != nil {
		log.Println("Maybe you're not running inside a tmux session?")
		return errors.Wrap(err, "opening tmux window")
	}
	// Wait to receive the tmux signal.
	if _, err := exec.Command("tmux", "wait", done).Output(); err != nil {
		return errors.Wrap(err, "failed to wait for tmux signal")
	}
	return nil
}

// The Explorer is the Tree structure on the left side of the TUI
type Explorer struct {
	ctx        context.Context
	app        *tview.Application
	container  *tview.Pages
	tree       *tview.TreeView
	root       *tview.TreeNode
	rb         *rebuilder.Rebuilder
	dex        rundex.Reader
	rundexOpts rundex.FetchRebuildOpts
	runs       map[string]rundex.Run
	buildDefs  rebuild.LocatableAssetStore
	butler     localfiles.Butler
}

func NewExplorer(ctx context.Context, app *tview.Application, dex rundex.Reader, rundexOpts rundex.FetchRebuildOpts, rb *rebuilder.Rebuilder, buildDefs rebuild.LocatableAssetStore, butler localfiles.Butler) *Explorer {
	e := Explorer{
		ctx:        ctx,
		app:        app,
		container:  tview.NewPages(),
		tree:       tview.NewTreeView(),
		root:       tview.NewTreeNode("root").SetColor(tcell.ColorRed),
		rb:         rb,
		dex:        dex,
		rundexOpts: rundexOpts,
		buildDefs:  buildDefs,
		butler:     butler,
	}
	e.tree.SetRoot(e.root).SetCurrentNode(e.root)
	e.container.AddPage("explorer", e.tree, true, true)
	return &e
}

func (e *Explorer) Container() tview.Primitive {
	return e.container
}

func makeCommandNode(name string, handler func()) *tview.TreeNode {
	return tview.NewTreeNode(name).SetColor(tcell.ColorDarkCyan).SetSelectedFunc(handler)
}

func stabilizeArtifact(in, out string, t rebuild.Target) error {
	orig, err := os.Open(in)
	if err != nil {
		return errors.Wrap(err, "opening input")
	}
	defer orig.Close()
	stabilized, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE, os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "opening output")
	}
	defer stabilized.Close()
	if err := archive.Stabilize(stabilized, orig, t.ArchiveType()); err != nil {
		return errors.Wrap(err, "running stabilize")
	}
	return nil
}

func diffArtifacts(ctx context.Context, butler localfiles.Butler, example rundex.Rebuild) error {
	if example.Artifact == "" {
		return errors.New("Rundex does not have the artifact, cannot find GCS path.")
	}
	t := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(example.Ecosystem),
		Package:   example.Package,
		Version:   example.Version,
		Artifact:  example.Artifact,
	}
	var rba, usa string
	{
		var err error
		if example.WasSmoketest() {
			rba, err = butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugRebuildAsset.For(t))
			if err != nil {
				return errors.Wrap(err, "fetching rebuild asset")
			}
			usa, err = butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugUpstreamAsset.For(t))
			if err != nil {
				return errors.Wrap(err, "fetching upstream asset")
			}
		} else {
			dir, err := os.MkdirTemp("", "*")
			if err != nil {
				return errors.Wrap(err, "creating tempdir")
			}
			defer os.RemoveAll(dir)
			{
				rba, err = butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.RebuildAsset.For(t))
				if err != nil {
					return errors.Wrap(err, "fetching rebuild asset")
				}
				// TODO: We should use the version of Stabilize used in the rebuild.
				stabilized := filepath.Join(dir, "stabilized-"+filepath.Base(rba))
				if err := stabilizeArtifact(rba, stabilized, t); err != nil {
					return errors.Wrap(err, "stabilizing rebuild")
				}
				rba = stabilized
			}
			{
				usa, err = butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugUpstreamAsset.For(t))
				if err != nil {
					return errors.Wrap(err, "fetching upstream asset")
				}
				// TODO: We should use the version of Stabilize used in the rebuild.
				stabilized := filepath.Join(dir, "stabilized-"+filepath.Base(usa))
				if err := stabilizeArtifact(usa, stabilized, t); err != nil {
					return errors.Wrap(err, "stabilizing upstream")
				}
				usa = stabilized
			}
		}
	}
	var script string
	args := fmt.Sprintf(" --no-progress --text-color=always %s %s 2>&1 | less -R", rba, usa)
	if _, err := exec.LookPath("diffoscope"); err == nil {
		script = "diffoscope" + args
	} else if _, err := exec.LookPath("uvx"); err == nil {
		script = "uvx diffoscope" + args
	} else if _, err := exec.LookPath("docker"); err == nil {
		dir := filepath.Dir(usa)
		script = fmt.Sprintf("docker run --rm -t --user $(id -u):$(id -g) -v %s:%s:ro registry.salsa.debian.org/reproducible-builds/diffoscope", dir, dir) + args
	} else {
		log.Println("No execution option found for diffoscope. Attempted {diffoscope,uvx,docker}")
		return errors.New("failed to run diffoscope")
	}
	if err := tmuxWait(script); err != nil {
		return errors.Wrap(err, "running diffoscope")
	}
	return nil
}

func (e *Explorer) showDetails(example rundex.Rebuild) {
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
	details.SetText(detailsYaml.String()).SetBackgroundColor(tcell.ColorDarkCyan).SetTitle("Execution details")
	modal.Show(e.app, e.container, details, modal.ModalOpts{Margin: 10})
}

func (e *Explorer) showLogs(ctx context.Context, example rundex.Rebuild) {
	if example.Artifact == "" {
		log.Println("Rundex does not have the artifact, cannot find GCS path.")
		return
	}
	t := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(example.Ecosystem),
		Package:   example.Package,
		Version:   example.Version,
		Artifact:  example.Artifact,
	}
	logs, err := e.butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugLogsAsset.For(t))
	if err != nil {
		log.Println(errors.Wrap(err, "downloading logs"))
		return
	}
	cmd := exec.Command("tmux", "new-window", fmt.Sprintf("cat %s | less", logs))
	if err := cmd.Run(); err != nil {
		log.Println(errors.Wrap(err, "failed to read logs"))
		if err.Error() == "exit status 1" {
			log.Println("Maybe you're not running inside a tmux session?")
		}
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
		if err := tmuxWait(fmt.Sprintf("%s %s", editor, e.buildDefs.URL(buildDefAsset).Path)); err != nil {
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
	e.rb.RunLocal(e.ctx, example, rebuilder.RunLocalOpts{Strategy: &newStrat})
	return nil
}
func (e *Explorer) RunBenchmark(bench string) {
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
	wdex.WriteRun(e.ctx, rundex.FromRun(schema.Run{
		ID:            runID,
		BenchmarkName: filepath.Base(bench),
		BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
		Type:          string(schema.SmoketestMode),
		Created:       ts.UnixMilli(),
	}))
	verdictChan, err := e.rb.RunBench(e.ctx, set, runID)
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
		wdex.WriteRebuild(e.ctx, rundex.Rebuild{
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
			node.AddChild(makeCommandNode("run local", func() {
				go e.rb.RunLocal(e.ctx, example, rebuilder.RunLocalOpts{})
			}))
			node.AddChild(makeCommandNode("restart && run local", func() {
				go func() {
					e.rb.Restart(e.ctx)
					e.rb.RunLocal(e.ctx, example, rebuilder.RunLocalOpts{})
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
				go e.showDetails(example)
			}))
			node.AddChild(makeCommandNode("logs", func() {
				go e.showLogs(e.ctx, example)
			}))
			node.AddChild(makeCommandNode("diff", func() {
				go func() {
					if err := diffArtifacts(e.ctx, e.butler, example); err != nil {
						log.Println(err.Error())
					}
				}()
			}))
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
			rebuilds, err := e.dex.FetchRebuilds(e.ctx, &rundex.FetchRebuildRequest{Runs: []string{runid}, Opts: e.rundexOpts, LatestPerPackage: true})
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

func (e *Explorer) makeRunGroupNode(benchName string, runs []string) *tview.TreeNode {
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

// LoadTree will query rundex for all the runs, then display them.
func (e *Explorer) LoadTree() error {
	e.root.ClearChildren()
	log.Printf("Fetching runs...")
	runs, err := e.dex.FetchRuns(e.ctx, rundex.FetchRunsOpts{})
	if err != nil {
		return err
	}
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
