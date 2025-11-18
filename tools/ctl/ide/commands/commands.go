// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/glob"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/google/oss-rebuild/tools/ctl/ide/assistant"
	"github.com/google/oss-rebuild/tools/ctl/ide/chatbox"
	"github.com/google/oss-rebuild/tools/ctl/ide/commandreg"
	"github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuildhistory"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextable"
	"github.com/google/oss-rebuild/tools/ctl/ide/rundextree"
	"github.com/google/oss-rebuild/tools/ctl/ide/textinput"
	"github.com/google/oss-rebuild/tools/ctl/ide/tmux"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

const (
	RundexReadParallelism = 10
	LLMRequestParallelism = 50
	expertPrompt          = `You are an expert in diagnosing build issues in multiple open source ecosystems. You will help diagnose why builds failed, or why the builds might have produced an artifact that differs from the upstream open source package. Provide clear and concise explantions of why the rebuild failed, and suggest changes that could fix the rebuild`
	containerName         = "oss-rebuild-debug-temp"
)

func removeContainer(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "docker", "container", "rm", "-f", name).Run()
}

func runLocal(ctx context.Context, executor build.Executor, prebuildConfig rebuild.PrebuildConfig, dex rundex.Reader, inp rebuild.Input) error {
	// Clean up any previous container. We don't check for errors, because a missing container is fine
	removeContainer(ctx, containerName)
	assets := rebuild.NewFilesystemAssetStore(memfs.New())
	h, err := executor.Start(ctx, inp, build.Options{
		// Use a constant BuildID to make sure we overwrite the container each run
		BuildID:     containerName,
		UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
		Resources: build.Resources{
			AssetStore: assets,
			ToolURLs: map[build.ToolType]string{
				build.TimewarpTool: "gs://" + path.Join(prebuildConfig.Bucket, prebuildConfig.Dir, "timewarp"),
			},
			BaseImageConfig: build.DefaultBaseImageConfig(),
		},
	})
	if err != nil {
		return errors.Wrap(err, "starting build")
	}
	go io.Copy(log.Default().Writer(), h.OutputStream())
	res, err := h.Wait(ctx)
	if err != nil {
		return errors.Wrap(err, "error while waiting")
	} else if res.Error != nil {
		return errors.Wrap(res.Error, "build failed")
	}
	// TODO: How should we export the artifacts? Maybe butler?
	// TODO: Log results to dex if it's writable
	return nil
}

func inferLocal(ctx context.Context, target rebuild.Target) (rebuild.Strategy, error) {
	cmd := exec.CommandContext(ctx, "go", "run", "./tools/ctl", "infer", "--ecosystem", string(target.Ecosystem), "--package", target.Package, "--version", target.Version, "--artifact", target.Artifact)
	cmd.Stderr = log.Default().Writer()
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrap(err, "running inference")
	}
	var oneOf schema.StrategyOneOf
	err = json.NewDecoder(bytes.NewBuffer(out)).Decode(&oneOf)
	if err != nil {
		log.Println("Broken strategy: " + string(out))
		return nil, errors.Wrap(err, "decoding strategy")
	}
	s, err := oneOf.Strategy()
	if err != nil {
		return nil, errors.Wrap(err, "decoding strategy")
	}
	return s, nil
}

func NewRebuildCmds(app *tview.Application, executor build.Executor, prebuildConfig rebuild.PrebuildConfig, modalFn modal.Fn, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository, cmdReg *commandreg.Registry) []commandreg.RebuildCmd {
	return []commandreg.RebuildCmd{
		{
			Short: "run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				s, err := inferLocal(ctx, example.Target())
				if err != nil {
					log.Println(err)
					return
				}
				if err := runLocal(ctx, executor, prebuildConfig, dex, rebuild.Input{Target: example.Target(), Strategy: s}); err != nil {
					log.Println(err)
					return
				}
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
				strat, err := newStrat.Strategy()
				if err != nil {
					log.Println(errors.Wrap(err, "decoding strategy"))
					return
				}
				if err := runLocal(ctx, executor, prebuildConfig, dex, rebuild.Input{Target: example.Target(), Strategy: strat}); err != nil {
					log.Println(err)
					return
				}
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
				logs, err := butler.Fetch(ctx, example.RunID, rebuild.DebugLogsAsset.For(example.Target()))
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
				path, err := butler.Fetch(ctx, example.RunID, diffoscope.DiffAsset.For(example.Target()))
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
			Short: "generate stabilizers from diff",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				// Generate the path exclusion stabilizers.
				var customStabs []archive.CustomStabilizerEntry
				{
					if _, err := butler.Fetch(ctx, example.RunID, rebuild.DebugUpstreamAsset.For(example.Target())); err != nil {
						log.Println(errors.Wrap(err, "downloading upstream"))
						return
					}
					if _, err := butler.Fetch(ctx, example.RunID, rebuild.RebuildAsset.For(example.Target())); err != nil {
						log.Println(errors.Wrap(err, "downloading rebuild"))
						return
					}
					// TODO: This would be unecessary if butler returned an AssetStore.
					assets, err := localfiles.AssetStore(example.RunID)
					if err != nil {
						log.Println(errors.Wrap(err, "creating asset store"))
						return
					}
					var upCS *archive.ContentSummary
					{
						usr, err := assets.Reader(ctx, rebuild.DebugUpstreamAsset.For(example.Target()))
						if err != nil {
							log.Println(errors.Wrap(err, "opening upstream"))
							return
						}
						defer usr.Close()
						upCS, err = archive.NewContentSummary(usr, example.Target().ArchiveType())
						if err != nil {
							log.Println(errors.Wrap(err, "summarizing upstream"))
							return
						}
					}
					var rbCS *archive.ContentSummary
					{
						rbr, err := assets.Reader(ctx, rebuild.RebuildAsset.For(example.Target()))
						if err != nil {
							log.Println(errors.Wrap(err, "opening rebuild"))
							return
						}
						defer rbr.Close()
						rbCS, err = archive.NewContentSummary(rbr, example.Target().ArchiveType())
						if err != nil {
							log.Println(errors.Wrap(err, "summarizing rebuild"))
							return
						}
					}
					exclusionStab := func(path, reason string) archive.CustomStabilizerEntry {
						return archive.CustomStabilizerEntry{
							Config: archive.CustomStabilizerConfigOneOf{
								ExcludePath: &archive.ExcludePath{
									Paths: []string{path},
								},
							},
							Reason: reason,
						}
					}
					left, diff, right := rbCS.Diff(upCS)
					for _, p := range left {
						customStabs = append(customStabs, exclusionStab(p, "Found in rebuild.\nFIXME: Explain why it's safe to ignore."))
					}
					for _, p := range diff {
						customStabs = append(customStabs, exclusionStab(p, "Found in both.\nFIXME: Explain why it's safe to ignore."))
					}
					for _, p := range right {
						customStabs = append(customStabs, exclusionStab(p, "Found in Upstream.\nFIXME: Explain why it's safe to ignore."))
					}
				}
				buildDefAsset := rebuild.BuildDef.For(example.Target())
				// Read current definition.
				var currentDef schema.BuildDefinition
				{
					if r, err := buildDefs.Reader(ctx, buildDefAsset); err == nil {
						d := yaml.NewDecoder(r)
						if d.Decode(&currentDef) != nil {
							log.Println(errors.Wrap(err, "failed to read existing build definition"))
							return
						}
					}
				}
				// Avoid adding a path exclusion if it already exists.
				existing := map[string]bool{}
				for _, s := range currentDef.CustomStabilizers {
					if s.Config.ExcludePath != nil {
						for _, p := range s.Config.ExcludePath.Paths {
							existing[p] = true
						}
					}
				}
				// Add the exclusions.
				for _, s := range customStabs {
					matchesExisting := false
					for pattern := range existing {
						if match, err := glob.Match(pattern, s.Config.ExcludePath.Paths[0]); err == nil && match {
							matchesExisting = true
							break
						}
					}
					if !matchesExisting {
						currentDef.CustomStabilizers = append(currentDef.CustomStabilizers, s)
					}
				}
				// Write the strategy and open for editing.
				{
					w, err := buildDefs.Writer(ctx, buildDefAsset)
					if err != nil {
						log.Println(errors.Wrapf(err, "opening build definition"))
						return
					}
					enc := yaml.NewEncoder(w)
					if enc.Encode(&currentDef) != nil {
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
				}
			},
		},
		{
			Short: "debug with ✨AI✨",
			DisabledMsg: func() string {
				if aiClient == nil {
					return "To enable AI features, provide a gcloud project with Vertex AI API enabled."
				}
				return ""
			},
			Func: func(ctx context.Context, example rundex.Rebuild) {
				var config *genai.GenerateContentConfig
				{
					config = &genai.GenerateContentConfig{
						Temperature:     genai.Ptr(float32(0.1)),
						MaxOutputTokens: int32(16000),
					}
					systemPrompt := []*genai.Part{
						{Text: expertPrompt},
					}
					config = llm.WithSystemPrompt(config, systemPrompt...)
				}
				s, err := assistant.NewAssistant(butler, aiClient, llm.GeminiFlash, config).Session(ctx, example)
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

func NewRebuildGroupCmds(app *tview.Application, executor build.Executor, prebuildConfig rebuild.PrebuildConfig, modalFn modal.Fn, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository, cmdReg *commandreg.Registry) []commandreg.RebuildGroupCmd {
	return []commandreg.RebuildGroupCmd{
		{
			Short: "Make benchmark",
			Func: func(ctx context.Context, rebuilds []rundex.Rebuild) {
				set := benchmark.PackageSet{}
				var total int
				packages := map[string]map[string]*benchmark.Package{}
				for _, r := range rebuilds {
					if packages[r.Ecosystem] == nil {
						packages[r.Ecosystem] = map[string]*benchmark.Package{}
					}
					if _, ok := packages[r.Ecosystem][r.Package]; !ok {
						packages[r.Ecosystem][r.Package] = &benchmark.Package{
							Ecosystem: r.Ecosystem,
							Name:      r.Package,
							Versions:  []string{},
							Artifacts: []string{},
						}
					}
					packages[r.Ecosystem][r.Package].Versions = append(packages[r.Ecosystem][r.Package].Versions, r.Version)
					if r.Artifact != "" {
						packages[r.Ecosystem][r.Package].Artifacts = append(packages[r.Ecosystem][r.Package].Artifacts, r.Artifact)
					}
					total++
				}
				for _, e := range packages {
					for _, p := range e {
						set.Packages = append(set.Packages, *p)
					}
				}
				set.Count = total
				tempFile, err := os.CreateTemp("", "benchmark-*.json")
				if err != nil {
					log.Println(errors.Wrap(err, "creating benchmark file"))
					return
				}
				defer tempFile.Close()
				e := json.NewEncoder(tempFile)
				e.SetIndent("", "  ")
				if err := e.Encode(set); err != nil {
					log.Println(errors.Wrap(err, "encoding benchmark file"))
					return
				}
				log.Println("Benchmark saved to: ", tempFile.Name())
			},
		},
		{
			Short: "Find pattern",
			Func: func(ctx context.Context, rebuilds []rundex.Rebuild) {
				pattern, mopts, inputChan := textinput.TextInput(textinput.TextInputOpts{Header: "Search Regex"})
				exitFunc := modalFn(pattern, mopts)
				input := <-inputChan
				log.Printf("Finding pattern \"%s\"", input)
				exitFunc()
				regex, err := regexp.Compile(input)
				if err != nil {
					log.Println(err.Error())
					return
				}
				p := pipe.FromSlice(rebuilds)
				p = p.ParDo(RundexReadParallelism, func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					_, err := butler.Fetch(context.Background(), in.RunID, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrap(err, "downloading logs"))
						return
					}
					out <- in
				})
				p = p.Do(func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					assets, err := localfiles.AssetStore(in.RunID)
					if err != nil {
						log.Println(errors.Wrapf(err, "creating asset store for runid: %s", in.RunID))
						return
					}
					r, err := assets.Reader(ctx, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrapf(err, "opening logs for %s", in.ID()))
						return
					}
					defer r.Close()
					// TODO: Maybe read the whole file into memory and do multi-line matching?
					scanner := bufio.NewScanner(r)
					for scanner.Scan() {
						line := scanner.Text()
						if regex.MatchString(line) {
							log.Printf("%s\n\t%s", in.ID(), line)
							out <- in
							break
						}
					}
					if err := scanner.Err(); err != nil {
						log.Println(errors.Wrap(err, "reading logs"))
					}
				})
				var found int
				for range p.Out() {
					found++
				}
				log.Printf("Found in %d/%d (%2.0f%%)", found, len(rebuilds), float32(found)/float32(len(rebuilds))*100)
			},
		},
		{
			Short: "Cluster using AI",
			DisabledMsg: func() string {
				if aiClient == nil {
					return "To enable AI features, provide a gcloud project with Vertex AI API enabled."
				}
				return ""
			},
			Func: func(ctx context.Context, rebuilds []rundex.Rebuild) {
				var config *genai.GenerateContentConfig
				{
					config = &genai.GenerateContentConfig{
						Temperature:     genai.Ptr(float32(0.1)),
						MaxOutputTokens: int32(16000),
					}
					systemPrompt := []*genai.Part{
						{Text: expertPrompt},
					}
					config = llm.WithSystemPrompt(config, systemPrompt...)
				}
				p1 := pipe.FromSlice(rebuilds).ParDo(RundexReadParallelism, func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					_, err := butler.Fetch(context.Background(), in.RunID, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrap(err, "downloading logs"))
						return
					}
					out <- in
				})
				// TODO: Instead of a ticker, gracefully handle retriable errors on the API.
				ticker := time.Tick(time.Second / 15) // The Gemini Flash limit is around 15 QPS.
				type summarizedRebuild struct {
					Rebuild rundex.Rebuild
					Summary string
				}
				p2 := pipe.ParInto(LLMRequestParallelism, p1, func(in rundex.Rebuild, out chan<- summarizedRebuild) {
					const uploadBytesLimit = 100_000
					assets, err := localfiles.AssetStore(in.RunID)
					if err != nil {
						log.Println(errors.Wrapf(err, "creating asset store for runid: %s", in.RunID))
						return
					}
					r, err := assets.Reader(ctx, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrapf(err, "opening logs for %s", in.ID()))
						return
					}
					defer r.Close()
					content, err := io.ReadAll(r)
					if err != nil {
						log.Println(errors.Wrap(err, "reading logs"))
						return
					}
					logs := string(content)
					if len(logs) > uploadBytesLimit {
						logs = "...(truncated)..." + logs[len(logs)-uploadBytesLimit:]
					}
					parts := []*genai.Part{
						{Text: "Please summarize this rebuild failure in one sentence."},
						{Text: logs},
					}
					if strings.Contains(in.Message, "content mismatch") {
						diffPath, err := butler.Fetch(ctx, in.RunID, diffoscope.DiffAsset.For(in.Target()))
						if err != nil {
							log.Println(errors.Wrap(err, "fetching diff"))
						} else {
							diffContent, err := os.ReadFile(diffPath)
							if err != nil {
								log.Println(errors.Wrap(err, "reading diff"))
							} else {
								diffStr := string(diffContent)
								if len(diffStr) > uploadBytesLimit {
									diffStr = "...(truncated)..." + diffStr[len(diffStr)-uploadBytesLimit:]
								}
								parts = append(parts, &genai.Part{Text: "The following is the diff of the rebuilt artifact against the original:\n" + diffStr})
							}
						}
					}
					<-ticker
					txt, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, parts...)
					if err != nil {
						log.Println(errors.Wrap(err, "sending message"))
						return
					}
					out <- summarizedRebuild{Rebuild: in, Summary: string(txt)}
					log.Println("Summary: ", txt)
				})
				var summaries []summarizedRebuild
				var parts []*genai.Part
				log.Printf("Summarizing %d rebuild failures", len(rebuilds))
				for s := range p2.Out() {
					summaries = append(summaries, s)
					if s.Summary == "" {
						continue
					}
					parts = append(parts, &genai.Part{Text: s.Summary})
				}
				log.Printf("Finished summarizing, Asking for categories based on %d summaries.", len(parts))
				// TODO: Give more structure to the expected output format to make it easier parsing the response.
				parts = append([]*genai.Part{{Text: "Based on the following error summaries, please provide 1 to 5 classes of failures you think are happening."}}, parts...)
				<-ticker
				rawFailureClasses, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, parts...)
				if err != nil {
					log.Println(errors.Wrap(err, "classifying summaries"))
					return
				}
				log.Println(rawFailureClasses)
				parts = []*genai.Part{{Text: "Please format this list of summaries into one line per summary. Do not include any of the following: syntax highlighting, numbering, bullet points, or other markdown."}, {Text: rawFailureClasses}}
				<-ticker
				failuresClasses, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, parts...)
				if err != nil {
					log.Println(errors.Wrap(err, "formatting classes"))
					return
				}
				classes := strings.Split(string(failuresClasses), "\n")
				log.Printf("Found %d classes: %s", len(classes), strings.Join(classes, "\n"))
				p3 := pipe.ParInto(LLMRequestParallelism, pipe.FromSlice(summaries), func(in summarizedRebuild, out chan<- rundex.Rebuild) {
					if in.Summary == "" {
						return
					}
					prompt := fmt.Sprintf("Given the following failure classes:\n%s\n\nAnd the following error summary:\n%s\n\nPlease classify the summary into one of the classes. Respond with only the class name.", string(failuresClasses), in.Summary)
					<-ticker
					className, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, &genai.Part{Text: prompt})
					if err != nil {
						log.Printf("Failed to classify summary for %s: %v", in.Rebuild.ID(), err)
						return
					}
					in.Rebuild.Message = strings.TrimSpace(string(className))
					out <- in.Rebuild
				})
				// TODO: Make the tree-generation code on explorer public and use that here, passing it into modalFn
				// Maybe the explorer should just have a factory method to generate a tree obj?
				tree := rundextree.New(app, modalFn, dex, rundex.FetchRebuildOpts{}, benches, cmdReg)
				var clusteredRebuilds []rundex.Rebuild
				for r := range p3.Out() {
					clusteredRebuilds = append(clusteredRebuilds, r)
				}
				tree.LoadRebuilds(clusteredRebuilds)
				modalFn(tree, modal.ModalOpts{Margin: 10})
			},
		},
	}

}

func NewGlobalCmds(app *tview.Application, executor build.Executor, prebuildConfig rebuild.PrebuildConfig, modalFn modal.Fn, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository, cmdReg *commandreg.Registry) []commandreg.GlobalCmd {
	return []commandreg.GlobalCmd{
		{
			Short:  "attach",
			Hotkey: 'a',
			Func: func(ctx context.Context) {
				if err := tmux.Start(fmt.Sprintf("docker exec -it %s sh", containerName)); err != nil {
					log.Println(err)
					return
				}
			},
		},
	}
}

func NewBenchmarkCmds(app *tview.Application, executor build.Executor, prebuildConfig rebuild.PrebuildConfig, modalFn modal.Fn, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository, cmdReg *commandreg.Registry) []commandreg.BenchmarkCmd {
	return []commandreg.BenchmarkCmd{
		{
			Short: "View by target",
			Func: func(ctx context.Context, benchName string) {
				all, err := benches.List()
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
				tracked := make(map[string]bool)
				var set benchmark.PackageSet
				{
					set, err = benches.Load(benchPath)
					if err != nil {
						log.Println(errors.Wrap(err, "reading benchmark"))
						return
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
					rebuilds, err = dex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{Bench: &set, Opts: rundex.FetchRebuildOpts{}, LatestPerPackage: true})
					if err != nil {
						log.Println(errors.Wrapf(err, "loading rebuilds"))
						return
					}
					log.Printf("Fetched %d rebuilds in %v", len(rebuilds), time.Since(start))
					slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
						return strings.Compare(a.ID(), b.ID())
					})
					rebuilds = slices.DeleteFunc(rebuilds, func(r rundex.Rebuild) bool {
						return !tracked[r.ID()]
					})
				}
				onSelect := func(rebuild rundex.Rebuild) {
					log.Println("Loading history for", rebuild.ID())
					t := rebuild.Target()
					rebuildsOfTarget, err := dex.FetchRebuilds(context.Background(), &rundex.FetchRebuildRequest{
						Target: &t,
						Opts:   rundex.FetchRebuildOpts{},
					})
					if err != nil {
						log.Println(errors.Wrap(err, "fetching rebuilds for target"))
						return
					}
					modalFn(rebuildhistory.New(modalFn, cmdReg, rebuildsOfTarget), modal.ModalOpts{Margin: 10})
				}
				table, err := rundextable.New(rebuilds, cmdReg, onSelect)
				if err != nil {
					log.Println(err)
					return
				}
				modalFn(table, modal.ModalOpts{Margin: 10})
			},
		},
	}
}
