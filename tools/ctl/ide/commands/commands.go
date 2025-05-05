// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/google/oss-rebuild/tools/ctl/ide/assistant"
	"github.com/google/oss-rebuild/tools/ctl/ide/chatbox"
	"github.com/google/oss-rebuild/tools/ctl/ide/choice"
	"github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/ide/tmux"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

// A modalFnType can be used to show an InputCaptureable. It returns an exit function that can be used to close the modal.
type modalFnType func(modal.InputCaptureable, modal.ModalOpts) func()

func NewRebuildCmds(app *tview.Application, rb *rebuilder.Rebuilder, modalFn modalFnType, butler localfiles.Butler, asst assistant.Assistant, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository) []RebuildCmd {
	return []RebuildCmd{
		{
			Short: "run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Short: "restart && run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				rb.Restart(ctx)
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Short: "edit and run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				buildDefAsset := rebuild.BuildDef.For(example.Target())
				var currentStrat schema.StrategyOneOf
				{
					if r, err := buildDefs.Reader(ctx, buildDefAsset); err == nil {
						d := yaml.NewDecoder(r)
						if d.Decode(&currentStrat) != nil {
							log.Println(errors.Wrap(err, "failed to read existing build definition"))
							return
						}
					} else {
						currentStrat = example.Strategy
						s, err := currentStrat.Strategy()
						if err != nil {
							log.Println(errors.Wrap(err, "unpacking StrategyOneOf"))
							return
						}
						// Convert this strategy to a workflow strategy if possible.
						if fable, ok := s.(rebuild.Flowable); ok {
							currentStrat = schema.NewStrategyOneOf(fable.ToWorkflow())
						}
					}
				}
				var newStrat schema.StrategyOneOf
				{
					w, err := buildDefs.Writer(ctx, buildDefAsset)
					if err != nil {
						log.Println(errors.Wrapf(err, "opening build definition"))
						return
					}
					if _, err = w.Write([]byte("# Edit the build definition below, then save and exit the file to begin a rebuild.\n")); err != nil {
						log.Println(errors.Wrapf(err, "writing comment to build definition file"))
						return
					}
					enc := yaml.NewEncoder(w)
					if enc.Encode(&currentStrat) != nil {
						log.Println(errors.Wrapf(err, "populating build definition"))
						return
					}
					w.Close()
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vim"
					}
					if err := tmux.Wait(fmt.Sprintf("%s %s", editor, buildDefs.URL(buildDefAsset).Path)); err != nil {
						log.Println(errors.Wrap(err, "editing build definition"))
						return
					}
					r, err := buildDefs.Reader(ctx, buildDefAsset)
					if err != nil {
						log.Println(errors.Wrap(err, "failed to open build definition after edits"))
						return
					}
					d := yaml.NewDecoder(r)
					if err := d.Decode(&newStrat); err != nil {
						log.Println(errors.Wrap(err, "manual strategy oneof failed to parse"))
						return
					}
				}
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{Strategy: &newStrat})
			},
		},
		{
			Hotkey: 'm',
			Short:  "metadata",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if deets, err := details.View(example); err != nil {
					log.Println(err.Error())
					return
				} else {
					modalFn(deets, modal.ModalOpts{Margin: 10})
				}
			},
		},
		{
			Hotkey: 'l',
			Short:  "logs",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if example.Artifact == "" {
					log.Println("Rundex does not have the artifact, cannot find GCS path.")
					return
				}
				logs, err := butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugLogsAsset.For(example.Target()))
				if err != nil {
					log.Println(errors.Wrap(err, "downloading logs"))
					return
				}
				if err := tmux.Start(fmt.Sprintf("cat %s | less", logs)); err != nil {
					log.Println(errors.Wrap(err, "failed to read logs"))
				}
			},
		},
		{
			Hotkey: 'd',
			Short:  "diff",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				path, err := butler.Fetch(ctx, example.RunID, example.WasSmoketest(), diffoscope.DiffAsset.For(example.Target()))
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
		{
			Short: "debug with ✨AI✨",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				s, err := asst.Session(ctx, example)
				if err != nil {
					log.Println(errors.Wrap(err, "creating session"))
					return
				}
				cb := chatbox.NewChatbox(app, s, chatbox.ChatBoxOpts{Welcome: "Debug with AI! Type /help for a list of commands.", InputHeader: "Ask the AI"})
				modalExit := modalFn(cb.Widget(), modal.ModalOpts{Margin: 10})
				go cb.HandleInput(ctx, "/debug")
				go func() {
					<-cb.Done()
					modalExit()
				}()
			},
		},
	}
}

func NewGlobalCmds(app *tview.Application, rb *rebuilder.Rebuilder, modalFn modalFnType, butler localfiles.Butler, asst assistant.Assistant, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository) []GlobalCmd {
	return []GlobalCmd{
		{
			Short:  "restart rebuilder",
			Hotkey: 'r',
			Func:   func(ctx context.Context) { rb.Restart(ctx) },
		},
		{
			Short:  "kill rebuilder",
			Hotkey: 'x',
			Func: func(_ context.Context) {
				rb.Kill()
			},
		},
		{
			Short:  "attach",
			Hotkey: 'a',
			Func: func(ctx context.Context) {
				if err := rb.Attach(ctx); err != nil {
					log.Println(err)
				}
			},
		},
		{
			Short:  "benchmark",
			Hotkey: 'b',
			Func: func(ctx context.Context) {
				var bench string
				{
					all, err := benches.List()
					if err != nil {
						log.Println(errors.Wrap(err, "listing benchmarks"))
						return
					}
					choice, opts, selected := choice.Choice(all)
					exitFunc := modalFn(choice, opts)
					defer exitFunc()
					bench = <-selected
				}
				wdex, ok := dex.(rundex.Writer)
				if !ok {
					log.Println(errors.New("Cannot run benchmark with non-local rundex client."))
					return
				}
				set, err := benchmark.ReadBenchmark(bench)
				if err != nil {
					log.Println(errors.Wrap(err, "reading benchmark"))
					return
				}
				var runID string
				{
					now := time.Now().UTC()
					runID = now.Format(time.RFC3339)
					wdex.WriteRun(ctx, rundex.FromRun(schema.Run{
						ID:            runID,
						BenchmarkName: filepath.Base(bench),
						BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
						Type:          string(schema.SmoketestMode),
						Created:       now,
					}))
				}
				verdictChan, err := rb.RunBench(ctx, set, runID)
				if err != nil {
					log.Println(errors.Wrap(err, "running benchmark"))
					return
				}
				var successes int
				for v := range verdictChan {
					if v.Message == "" {
						successes += 1
					}
					wdex.WriteRebuild(ctx, rundex.NewRebuildFromVerdict(v, "local", runID, time.Now().UTC()))
				}
				log.Printf("Finished benchmark %s with %d successes.", bench, successes)
			},
		},
	}
}
