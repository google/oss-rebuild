// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/fatih/color"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

const uploadBytesLimit = 100_000

func getRepoTree(r *git.Repository, commitHash string) (*object.Tree, error) {
	// Get the commit object
	hash := plumbing.NewHash(commitHash)
	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, errors.Wrap(err, "getting commit object")
	}
	// Get the tree for the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, errors.Wrap(err, "getting tree for commit")
	}
	return tree, nil
}

func getRepoFile(tree *object.Tree, path string) (string, error) {
	ent, err := tree.FindEntry(path)
	if err != nil {
		return "", err
	}
	if !ent.Mode.IsFile() {
		return "", errors.New("path does not refer to a file")
	}
	f, err := tree.TreeEntryFile(ent)
	if err != nil {
		return "", err
	}
	return f.Contents()
}

func listRepoFiles(tree *object.Tree, path string) ([]string, error) {
	if path == "" {
		path = "."
	}
	var pathTree *object.Tree
	if path != "." {
		ent, err := tree.FindEntry(path)
		if err != nil {
			return nil, err
		}
		if ent.Mode != filemode.Dir {
			return nil, errors.New("path does not refer to a dir")
		}
		pathTree, err = tree.Tree(path)
		if err != nil {
			return nil, err
		}
	} else {
		pathTree = tree
	}
	var names []string
	for _, ent := range pathTree.Entries {
		if ent.Mode.IsFile() {
			names = append(names, ent.Name)
		} else {
			names = append(names, ent.Name+"/")
		}
	}
	return names, nil
}

func locationFromStrategyOneOf(oneof *schema.StrategyOneOf) (*rebuild.Location, error) {
	s, err := oneof.Strategy()
	if err != nil {
		return nil, err
	}
	val := reflect.ValueOf(s)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	loc := val.FieldByName("Location")
	if !loc.IsValid() || !loc.CanInterface() {
		return nil, errors.New("strategy doesn't have Location field")
	}
	if l, ok := loc.Interface().(rebuild.Location); !ok {
		return nil, errors.New("the Location field isn't a rebuild.Location")
	} else {
		return &l, nil
	}
}

type defaultAgent struct {
	t           rebuild.Target
	deps        *AgentDeps
	repo        *git.Repository
	loc         rebuild.Location
	iterHistory []*schema.AgentIteration
	thoughts    []thoughtData
	tools       []ai.ToolRef
	assets      rebuild.AssetStore
}

func NewDefaultAgent(t rebuild.Target, deps *AgentDeps) *defaultAgent {
	a := &defaultAgent{t: t, deps: deps, assets: rebuild.NewFilesystemAssetStore(memfs.New())}
	a.registerTools()
	return a
}

func (a *defaultAgent) InitializeFromIteration(ctx context.Context, initialIteration *schema.AgentIteration) error {
	loc, err := locationFromStrategyOneOf(initialIteration.Strategy)
	if err != nil {
		return errors.Wrap(err, "parsing previous iteration")
	}
	a.repo, err = rebuild.LoadRepo(ctx, a.t.Package, memory.NewStorage(), memfs.New(), git.CloneOptions{URL: loc.Repo, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	if err != nil {
		return errors.Wrap(err, "loading repo")
	}
	w, err := a.repo.Worktree()
	if err != nil {
		return errors.Wrap(err, "getting worktree")
	}
	err = w.Checkout(&git.CheckoutOptions{
		Hash: plumbing.NewHash(loc.Ref),
	})
	if err != nil {
		errors.Wrap(err, "checkout")
	}
	a.RecordIteration(initialIteration)
	a.loc = *loc
	return nil
}

func (a *defaultAgent) metadata(ctx context.Context, obliviousID string) (rebuild.ReadOnlyAssetStore, error) {
	metadata, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, obliviousID), fmt.Sprintf("gs://%s", a.deps.MetadataBucket))
	return metadata, errors.Wrap(err, "creating metadata store")
}

func (a *defaultAgent) logs(ctx context.Context, obliviousID string) (io.ReadCloser, error) {
	meta, err := a.metadata(ctx, obliviousID)
	if err != nil {
		return nil, err
	}
	r, err := meta.Reader(ctx, rebuild.BuildInfoAsset.For(a.t))
	if err != nil {
		return nil, errors.Wrap(err, "reading build info")
	}
	bi := new(rebuild.BuildInfo)
	if json.NewDecoder(r).Decode(bi) != nil {
		return nil, errors.Wrap(err, "parsing build info")
	}
	if bi.BuildID == "" {
		return nil, errors.New("BuildID is empty, cannot read gcb logs")
	}
	client, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating gcs client")
	}
	obj := client.Bucket(a.deps.LogsBucket).Object(gcb.MergedLogFile(bi.BuildID))
	return obj.NewReader(ctx)
}

func (a *defaultAgent) registerTools() {
	a.tools = []ai.ToolRef{
		genkit.DefineTool(a.deps.Genkit, "read_repo_file", "Fetch the content of the file from the source repository",
			func(ctx *ai.ToolContext, path struct {
				Path string `jsonschema_description:"Path of the file to be read, relative to the repository root"`
			}) (string, error) {
				log.Printf("calling read_repo_file(%s)", path.Path)
				if tr, err := getRepoTree(a.repo, a.loc.Ref); err != nil {
					return "", err
				} else {
					return getRepoFile(tr, path.Path)
				}
			},
		),
		genkit.DefineTool(a.deps.Genkit, "list_repo_files", "Fetch the list of file from the repository",
			func(ctx *ai.ToolContext, path struct {
				Path string `jsonschema_description:"Path of the directory to be read, relative to the repository root. Use . to represent the root."`
			}) ([]string, error) {
				log.Printf("calling list_repo_files(%s)", path.Path)
				tr, err := getRepoTree(a.repo, a.loc.Ref)
				if err != nil {
					return nil, err
				}
				return listRepoFiles(tr, path.Path)
			},
		),
		genkit.DefineTool(a.deps.Genkit, "read_logs_end", "Read tail of the logs from the previous build. If the logs are large, they may be truncated providing only the tail.",
			func(ctx *ai.ToolContext, _ struct{}) (string, error) {
				log.Printf("calling read_logs_end()")
				if len(a.iterHistory) == 0 {
					return "Can't read logs because there was no previous build execution.", nil
				}
				prev := a.iterHistory[len(a.iterHistory)-1]
				r, err := a.logs(ctx, prev.ObliviousID)
				if err != nil {
					return "", errors.Wrap(err, "reading logs")
				}
				defer r.Close()
				b, err := io.ReadAll(r)
				logs := string(b)
				if len(logs) > uploadBytesLimit {
					logs = "...(truncated)..." + logs[len(logs)-uploadBytesLimit:]
				}
				return logs, err
			},
		),
	}
}

func (a *defaultAgent) proposeNormalInference(ctx context.Context) (*schema.StrategyOneOf, error) {
	wt := memfs.New()
	str := memory.NewStorage()
	s, err := inferenceservice.Infer(
		ctx,
		schema.InferenceRequest{
			Ecosystem: a.t.Ecosystem,
			Package:   a.t.Package,
			Version:   a.t.Version,
			Artifact:  a.t.Artifact,
		},
		&inferenceservice.InferDeps{
			HTTPClient: http.DefaultClient,
			GitCache:   nil,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{
					Worktree: wt,
					Storer:   str,
				}
			},
		},
	)
	if err != nil {
		return nil, errors.Wrap(err, "inferring initial strategy")
	}
	a.repo, err = git.Open(str, wt)
	if err != nil {
		return nil, errors.Wrap(err, "opening infered repo")
	}
	l, err := locationFromStrategyOneOf(s)
	if err != nil {
		return nil, errors.Wrap(err, "extracting location")
	}
	a.loc = *l
	return s, nil
}

func (a *defaultAgent) genericPrompt() []string {
	return []string{
		"Please diagnose this rebuild failure and propose an updated build description.",
		fmt.Sprintf("You are attempting to rebuild %#v", a.t),
		"You SHOULD NOT change the repo in the location. Even if you do, that will be overwritten when we attempt to execute the build again.",
		"You SHOULD NOT change the ref in the location.",
		"To debug the build, you might want to use the read_build_logs tool to view the build errors to understand what's going wrong.",
		"You might also want to inspect the contents of the source repo using the read_repo_file or list_repo_files tools.",
	}
}

func (a *defaultAgent) diagnoseOnly() []string {
	return []string{
		"Please explain what went wrong with the rebuild, and what might need to be changed to resolve the build.",
		"You can include in-line code snippets, but the overall description should only be two or three sentences.",
		"Another LLM will use your diagnosis to propose a fix.",
	}
}

func (a *defaultAgent) outputOnlyScript() []string {
	return []string{
		"When responding, please only respond with the bash script. Do not include any english text before or after the bash script, and do not include any formatting.",
		"You should include your reasoning as comments inside the bash script.",
		"These comments should sufficiently explain to a future reader how you got to this bash script compared to the original script.",
		"DO NOT include markdown backtics, or ANY formatting besides the raw content of the bash script.",
	}
}

func (a *defaultAgent) outputReasoningAndScript() []string {
	return []string{
		"When responding, please include both your reasoning and the updated buildscript.",
		"The reasoning should sufficiently explain to a future reader how you got to this bash script compared to the original script.",
		"Both the reasoning and script should be prefixed with a label. DO NOT include any other text besides these. For example:",
		"REASONING: The baz version has to be updated to version 1.2.3 to support foobar",
		"SCRIPT:",
		"#/bin/bash",
		"baz update --version=1.2.3",
		"baz build /src/...",
	}
}

func (a *defaultAgent) ecosystemExpertise() []string {
	// TODO: Actually implement this
	switch a.t.Ecosystem {
	case rebuild.Maven:
		return []string{}
	case rebuild.Debian:
		return []string{}
	default:
		return []string{}
	}
}

func (a *defaultAgent) historyContext(ctx context.Context) []string {
	prompt := []string{
		"# History",
		"Here are the details of the previous attempt.",
		"You can only control the build script, but other details are included to help you diagnose the failure.",
	}
	if len(a.iterHistory) > 0 {
		iteration := a.iterHistory[len(a.iterHistory)-1]
		if iteration.Strategy == nil {
			return nil
		}
		s, err := iteration.Strategy.Strategy()
		if err != nil {
			log.Printf("Previous iteration had no strategy: %v", err)
			return nil
		}
		var script string
		{
			inst, err := s.GenerateFor(a.t, rebuild.BuildEnv{
				TimewarpHost:           "localhost:8081",
				HasRepo:                false,
				PreferPreciseToolchain: false,
			})
			if err != nil {
				log.Printf(": %v", err)
				return nil
			}
			script = inst.Deps + "\n" + inst.Build
		}
		prompt = append(prompt,
			"## Build script:",
			"",
			"This is the content you can control, need to focus, on and need to update",
			"```bash",
			script,
			"```",
			"",
			"## Error message:",
			"```",
			iteration.Result.ErrorMessage,
			"```",
		)
		var dockerfile string
		{
			inp := rebuild.Input{Target: a.t, Strategy: s}
			resources := build.Resources{
				ToolURLs: map[build.ToolType]string{
					// TODO: Make a dummy URL for this, it won't actually be executed.
					build.TimewarpTool: "https://storage.googleapis.com/google-rebuild-bootstrap-tools/v0.0.0-20250428204534-b35098b3c7b7/timewarp",
				},
				BaseImageConfig: build.DefaultBaseImageConfig(),
			}
			plan, err := local.NewDockerRunPlanner().GeneratePlan(ctx, inp, build.PlanOptions{
				UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
				Resources:   resources,
			})
			if err == nil {
				dockerfile = plan.Script
			}
			if dockerfile != "" {
				prompt = append(
					prompt,
					"",
					"## Dockerfile",
					"This is for debugging purposes only. Do not include this file's contents in your response):",
					"",
					"```dockerfile",
					dockerfile,
					"```",
					"",
				)
			}
		}
	}
	if len(a.thoughts) > 0 {
		prompt = append(prompt,
			"",
			"## Thoughts so far",
			"Here are the thoughts you've had so far",
		)
		for i, t := range a.thoughts {
			prompt = append(prompt,
				"",
				fmt.Sprintf("### Thought %d", i+1),
				t.Diagnostic,
				"```bash",
				t.UpdatedScript,
				"```",
			)
		}
	}
	return prompt
}

func (a *defaultAgent) makePrompt(ctx context.Context) []string {
	prompt := a.genericPrompt()
	prompt = append(prompt, a.outputOnlyScript()...)
	prompt = append(prompt, a.ecosystemExpertise()...)
	prompt = append(prompt, a.historyContext(ctx)...)
	return prompt
}

func (a *defaultAgent) generate(ctx context.Context, prompt []string, opts ...ai.GenerateOption) (*ai.ModelResponse, error) {
	var err error
	var modelResp *ai.ModelResponse
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		opts = append([]ai.GenerateOption{ai.WithPrompt(strings.Join(prompt, "\n"))}, opts...)
		modelResp, err = genkit.Generate(
			ctx,
			a.deps.Genkit,
			opts...,
		)
	}()
	return modelResp, err
}

func (a *defaultAgent) makeDiagnosticPrompt() []string {
	return append(a.genericPrompt(), a.diagnoseOnly()...)
}

// One "cycle" of the LLM produces a thoughtData
type thoughtData struct {
	BasedOnIteration int    // The index in iterationHistory on which this thought is based.
	Diagnostic       string // The reasoning of why things were broken, and what might need to be fixed.
	UpdatedScript    string // The updated script.
}

func (a *defaultAgent) proposeAgentInference(ctx context.Context) (*schema.StrategyOneOf, error) {
	if len(a.iterHistory) == 0 {
		return nil, errors.New("proposeAgentInferece needs an previous iteration to work off of")
	}
	thought := thoughtData{
		BasedOnIteration: len(a.iterHistory) - 1,
	}
	// We have a dedicated diagnostic step to make sure we keep a history of what the AI thinks the problems are.
	{ // Diagnose
		log.Println("Asking the LLM to diagnose the failure and describe a fix...")
		p := a.makeDiagnosticPrompt()
		log.Println("Prompt:\n", color.YellowString(strings.Join(p, "\n")))
		modelResp, err := a.generate(ctx, p, ai.WithMaxTurns(a.deps.MaxTurns), ai.WithTools(a.tools...))
		if err != nil {
			return nil, errors.Wrap(err, "diagnose")
		}
		thought.Diagnostic = modelResp.Text()
		log.Printf("Gemini says:\n%s", color.CyanString(thought.Diagnostic))
	}
	var rawScript string
	{ // Implement
		log.Println("Asking the LLM to hypothesize a fix")
		p := a.makePrompt(ctx)
		p = append(p, "An expert reviewed this failure and gave these instructions for fixing it:", thought.Diagnostic)
		log.Println("Prompt:\n", color.YellowString(strings.Join(p, "\n")))
		// TODO: Switch the prompt to outputReasoningAndScript for structured reasoning.
		modelResp, err := a.generate(
			ctx,
			p,
			ai.WithMaxTurns(a.deps.MaxTurns),
		)
		if err != nil {
			return nil, errors.Wrap(err, "hypothesize")
		}
		rawScript = modelResp.Text()
		log.Printf("Gemini says:\n%s", color.CyanString(rawScript))
	}
	{
		modelResp, err := a.generate(
			ctx,
			[]string{
				"You are now a single-purpose, pure Bash script generator.",
				"Your entire output must be a single, raw, ready-to-execute Bash script.",
				"You must not include any surrounding text, explanations, markdown formatting (like ```bash or ```), titles, or conversational filler.",
				"Start your response immediately with the first line of the Bash script.",
				"From the following llm response, extract only the shell commands to be run and exclude any commands used to clone, checkout, and navigate to the git repo:",
				rawScript,
			},
			ai.WithModelName("vertexai/gemini-2.5-flash"),
		)
		if err != nil {
			return nil, errors.Wrap(err, "ai formatting")
		}
		// TODO: Check the script for invalid sequences, like EOS or EOF.
		script := modelResp.Text()
		if strings.HasPrefix(script, "```") {
			lines := strings.Split(script, "\n")
			lines = lines[1:]
			script = strings.Join(lines, "\n")
		}
		script = strings.Replace(script, "```", "", -1)
		thought.UpdatedScript = script
		log.Printf("After formatting: %s", color.WhiteString(thought.UpdatedScript))
	}
	a.thoughts = append(a.thoughts, thought)
	// TODO: Try to format the bash script into a structured strategy?
	strat := rebuild.ManualStrategy{
		Location: a.loc,
		Deps:     "echo 'running deps'",
		Build:    thought.UpdatedScript,
	}
	stratOneOf := schema.NewStrategyOneOf(&strat)
	return &stratOneOf, nil
}

func (a *defaultAgent) Propose(ctx context.Context) (*schema.StrategyOneOf, error) {
	// For the first iteration, use our regular inference logic.
	// This allows the agent to benefit from the rest of our infrence improvements.
	if len(a.iterHistory) == 0 {
		return a.proposeNormalInference(ctx)
	} else {
		return a.proposeAgentInference(ctx)
	}
}

func (a *defaultAgent) RecordIteration(i *schema.AgentIteration) {
	a.iterHistory = append(a.iterHistory, i)
}
