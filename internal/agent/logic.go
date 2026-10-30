// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"path"
	"reflect"
	"runtime/debug"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/buildpolicy"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
	"google.golang.org/grpc/status"
)

const uploadBytesLimit = 100_000

// Tool output shown to the model. A shell result keeps its first and last
// bytes so one verbose command cannot crowd the exchange: at ~4 bytes per
// token, a 40-call budget of 16KB results is ~160k tokens.
const (
	toolOutputHead = 4_000
	toolOutputTail = 12_000
	logsTailLimit  = 32_000 // read_logs_end keeps the tail alone, its value is the end of the log
	maxToolTurns   = 100    //  bounds the tool calls in one iteration, after which model is told to answer directly
)

// inferenceTimeout bounds heuristic inference, like the inference service's 14
// minute request timeout.
const inferenceTimeout = 15 * time.Minute

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
	t                rebuild.Target
	deps             *AgentDeps
	repo             *git.Repository
	loc              rebuild.Location
	iterHistory      []*schema.AgentIteration
	thoughts         []thoughtData
	assets           rebuild.AssetStore
	gitTools         []*llm.FunctionDefinition
	sideUsage        schema.TokenUsage       // accumulates LLM token consumption from calls made outside the main chat
	inferenceFailure string                  // inference failure a toolchain-only first attempt stood in for, shown to the model
	refusals         []buildpolicy.Violation // shown so the model avoids them
	reviewer         buildpolicy.Reviewer    // buildpolicy.Run, nil without a client
	refMoves         []refMove               // set_source_ref calls, in order
	refMoved         bool                    // deps must be re-inferred
	reinfer          inferFunc               // inferAt, or a test's fake
	ff               *freeFormDriver         // nil outside free-form mode
	newChat          chatMaker
}

// chatMaker starts a session-model chat with a system prompt and tools.
type chatMaker func(ctx context.Context, system string, tools []*llm.FunctionDefinition) (*llm.Chat, error)

// enableFreeForm makes proposals come from free-form sessions.
func (a *defaultAgent) enableFreeForm(runner *ScratchRunner, newChat chatMaker) {
	a.newChat = newChat
	a.ff = newFreeFormDriver(a, runner, a.openFreeForm)
}

func NewDefaultAgent(t rebuild.Target, deps *AgentDeps) *defaultAgent {
	a := &defaultAgent{t: t, deps: deps, assets: rebuild.NewFilesystemAssetStore(memfs.New())}
	a.gitTools = llm.GitTools(a.repoProvider)
	a.reinfer = a.inferAt
	if deps != nil && deps.GenaiClient != nil {
		a.reviewer = func(ctx context.Context, in buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
			return buildpolicy.Run(ctx, deps.GenaiClient, cmp.Or(deps.Model, llm.GeminiPro), in)
		}
	}
	return a
}

// repoProvider gives the git tools the repo and the current ref, read live so
// a ref move applies.
func (a *defaultAgent) repoProvider() (*git.Repository, string) {
	return a.repo, a.loc.Ref
}

func (a *defaultAgent) InitializeFromIteration(ctx context.Context, initialIteration *schema.AgentIteration) error {
	loc, err := locationFromStrategyOneOf(initialIteration.Strategy)
	if err != nil {
		return errors.Wrap(err, "parsing previous iteration")
	}
	wt := memfs.New()
	repo, err := rebuild.LoadRepo(ctx, a.t.Package, gitx.NewInMemoryStorer(), wt, git.CloneOptions{URL: loc.Repo, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	if err != nil {
		return errors.Wrap(err, "loading repo")
	}
	if a.repo, err = git.Open(repo.Storer, wt); err != nil {
		return errors.Wrap(err, "opening repo")
	}
	w, err := a.repo.Worktree()
	if err != nil {
		return errors.Wrap(err, "getting worktree")
	}
	err = w.Checkout(&git.CheckoutOptions{
		Hash: plumbing.NewHash(loc.Ref),
	})
	if err != nil {
		return errors.Wrap(err, "checkout")
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
	obj := a.deps.GCSClient.Bucket(a.deps.LogsBucket).Object(gcb.MergedLogFile(bi.BuildID))
	return obj.NewReader(ctx)
}

// singleEnv reports whether the session runs entirely in the build container,
// with no host shell tool.
func (a *defaultAgent) singleEnv() bool {
	return a.deps.SingleEnv
}

func (a *defaultAgent) getTools() []*llm.FunctionDefinition {
	tools := append(a.gitTools, a.readLogsTool(), a.setSourceRefTool())
	if a.deps.Shell != nil {
		if !a.singleEnv() {
			tools = append(tools, a.runOnHostTool())
		}
		tools = append(tools, a.runInContainerTool())
	}
	return tools
}

func (a *defaultAgent) readLogsTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "read_logs_end",
			Description: fmt.Sprintf("Read the tail of the previous build's log: the last %dKB, with a marker for what was cut before it.", logsTailLimit/1000),
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: map[string]*genai.Schema{},
				Required:   []string{},
			},
			Response: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"logs":  {Type: genai.TypeString, Description: "The tail end of the build logs"},
					"error": {Type: genai.TypeString, Description: "The error listing the requested path, if unsuccessful"},
				},
			},
		},
		Function: func(ctx context.Context, args map[string]any) genai.FunctionResponse {
			logs, err := a.readPreviousBuildLogs(ctx)
			if err != nil {
				return genai.FunctionResponse{
					Name: "read_logs_end", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"logs":  "",
						"error": err.Error(),
					},
				}
			}
			if len(logs) > logsTailLimit {
				logs = fmt.Sprintf("...[%d earlier bytes omitted]...\n", len(logs)-logsTailLimit) + logs[len(logs)-logsTailLimit:]
			}
			return genai.FunctionResponse{
				Name: "read_logs_end", // Name must match the FunctionDeclaration
				Response: map[string]any{
					"logs":  logs,
					"error": "",
				},
			}
		},
	}
}

// readPreviousBuildLogs returns the previous iteration's build logs from
// wherever that build actually ran, routed on ObliviousID (present only when
// the iteration was recorded from GCB).
// NOTE: A failed scratch-mode confirmation carries an ObliviousID, so its GCB
// logs win over the retained (successful) local build output.
func (a *defaultAgent) readPreviousBuildLogs(ctx context.Context) (string, error) {
	if len(a.iterHistory) > 0 {
		if prev := a.iterHistory[len(a.iterHistory)-1]; prev.ObliviousID != "" {
			r, err := a.logs(ctx, prev.ObliviousID)
			if err != nil {
				return "", errors.Wrap(err, "reading logs")
			}
			defer r.Close()
			b, err := io.ReadAll(r)
			if err != nil {
				return "", err
			}
			return string(b), nil
		}
	}
	if a.deps.Shell != nil {
		logs, err := a.deps.Shell.LastBuildLogs(ctx)
		if err != nil {
			return "", errors.Wrap(err, "reading logs")
		}
		return string(logs), nil
	}
	return "", errors.New("can't read logs because there was no previous build execution")
}

const runCommandTimeoutMax = 900

func (a *defaultAgent) runOnHostTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "run_on_host",
			Description: "Run a shell command on the build VM host (interpreted by /bin/sh -c). The host is minimal: docker, busybox coreutils and wget only, with no language runtimes. Use run_in_container for anything needing the build environment. Use the host to inspect docker images and containers and to retain or analyze files from previous build attempts e.g. ./builds/<build-id>/ containing the attempt's build.sh and out/ directory. After a content mismatch the upstream artifact, the stabilized form of both artifacts, and the diffr and stabilize tools are left on the host at the paths the mismatch report gives. The stabilize tool takes -enable-passes and -disable-passes to apply a subset of verification's stabilizers. The most recent build's container is retained as rb-<build-id> and can be identified with `docker ps -a --filter name=rb-` but all prior build containers are removed once the next build is triggered. Returns the merged stdout/stderr (a large output keeps only its head and tail, omitted_bytes counts the cut) and the exit code.",
			Parameters:  shellCommandParams(),
			Response:    shellCommandResponse(),
		},
		Function: func(ctx context.Context, args map[string]any) genai.FunctionResponse {
			return a.runShell(ctx, "run_on_host", args, nil)
		},
	}
}

// runInContainerDescription names the container's mounts in single-env mode,
// where there is no host shell.
func (a *defaultAgent) runInContainerDescription() string {
	desc := "Run a shell command inside the retained build container (rb-<build-id>), which carries the build's toolchain, source tree, and output from the most recent attempt. Use it to analyze the build environment, inspect intermediate state, or test a fix in the real build environment before proposing a new build. Each call is an independent 'bin/sh -c' starting in /src (the host directory ./builds/<build-id>/src): the working directory and shell environment reset between calls, filesystem changes persist. Requires a prior build attempt (exits 125 otherwise). Returns the merged stdout/stderr (a large output keeps only its head and tail; omitted_bytes counts the cut) and the exit code."
	if a.singleEnv() {
		desc = "Run a shell command inside the retained build container (rb-<build-id>), which carries the build's toolchain, source tree, and output from the most recent attempt. Everything you need is here: the rebuild at /out/rebuild, diffr at " + scratch.ContainerDiffrPath() + ", the upstream artifact under " + scratch.ContainerUpstreamDir + ", and every attempt's build.sh, out/, and src/ under " + scratch.ContainerBuildsDir + " (all read-only except the working tree). Use it to inspect the build environment or test a fix before proposing a new build. Each call is an independent 'bin/sh -c' starting in /src: the working directory and shell environment reset between calls, filesystem changes persist. Requires a prior build attempt (exits 125 otherwise). Returns the merged stdout/stderr (a large output keeps only its head and tail; omitted_bytes counts the cut) and the exit code."
	}
	return desc
}

func (a *defaultAgent) runInContainerTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "run_in_container",
			Description: a.runInContainerDescription(),
			Parameters:  shellCommandParams(),
			Response:    shellCommandResponse(),
		},
		Function: func(ctx context.Context, args map[string]any) genai.FunctionResponse {
			return a.runShell(ctx, "run_in_container", args, dockerExecInContainerScript)
		},
	}
}

// runShell dispatches a shell command to the scratch VM for tool `name`. When
// wrap is non-nil the raw command is transformed before dispatch (e.g. to exec
// it inside the build container). It normalizes the timeout, cuts large
// output to its ends, and shapes the response the LLM expects.
// NOTE: The runner fetches the last uploadBytesLimit bytes, so the head of a
// still larger output is the head of that window.
func (a *defaultAgent) runShell(ctx context.Context, name string, args map[string]any, wrap func(string) string) genai.FunctionResponse {
	command, _ := args["command"].(string)
	if command == "" {
		return shellResponse(name, "", 0, "command is required", 0)
	}
	timeout, terr := shellTimeout(args)
	if terr != "" {
		return shellResponse(name, "", 0, terr, 0)
	}
	run := func(script string, timeout int) (int, string, error) {
		if wrap != nil {
			script = wrap(script)
		}
		return a.deps.Shell.RunCommand(ctx, script, timeout)
	}
	out := fmt.Sprintf("/tmp/.rb-tool-%016x", rand.Uint64())
	exitCode, output, err := run(detachOutput(command, out), timeout)
	if errors.As(err, new(commandTimeout)) {
		// A timeout can kill the shell before its replay.
		_, back, rerr := run(readbackScript(out), readbackTimeout)
		if rerr != nil {
			log.Printf("%s: reading back a timed-out call's output: %v", name, rerr)
		}
		output = cmp.Or(back, output)
	}
	output, omitted := truncateEnds(output, cmp.Or(a.deps.ShellHeadBytes, toolOutputHead), cmp.Or(a.deps.ShellTailBytes, toolOutputTail))
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	return shellResponse(name, output, exitCode, errStr, omitted)
}

// truncateEnds keeps the first head and last tail bytes of s with a marker
// for the cut between them, returning how many bytes it omitted.
func truncateEnds(s string, head, tail int) (string, int) {
	if len(s) <= head+tail {
		return s, 0
	}
	omitted := len(s) - head - tail
	return s[:head] + fmt.Sprintf("\n...[%d bytes omitted, narrow the output with head, tail, or grep]...\n", omitted) + s[len(s)-tail:], omitted
}

// shellTimeout reads the optional timeout_seconds arg, clamping to
// runCommandTimeoutMax. It returns a non-empty error string for a non-numeric
// value.
func shellTimeout(args map[string]any) (int, string) {
	var timeout int
	switch v := args["timeout_seconds"].(type) {
	case nil:
	case float64:
		timeout = int(v)
	case int:
		timeout = v
	default:
		return 0, fmt.Sprintf("timeout_seconds must be a number, got %T", v)
	}
	if timeout > runCommandTimeoutMax {
		timeout = runCommandTimeoutMax
	}
	return timeout, ""
}

// shellResponse shapes a run_on_host / run_in_container FunctionResponse. Name
// must match the FunctionDeclaration.
func shellResponse(name, output string, exitCode int, errStr string, omitted int) genai.FunctionResponse {
	return genai.FunctionResponse{
		Name:     name,
		Response: map[string]any{"output": output, "exit_code": exitCode, "error": errStr, "omitted_bytes": omitted},
	}
}

func shellCommandParams() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"command":         {Type: genai.TypeString, Description: "The shell command to run (interpreted by /bin/sh -c)"},
			"timeout_seconds": {Type: genai.TypeInteger, Description: fmt.Sprintf("Optional command timeout in seconds (default 300, max %d)", runCommandTimeoutMax)},
		},
		Required: []string{"command"},
	}
}

func shellCommandResponse() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"output":        {Type: genai.TypeString, Description: "The merged stdout/stderr of the command"},
			"exit_code":     {Type: genai.TypeInteger, Description: "The command's exit code"},
			"error":         {Type: genai.TypeString, Description: "The error running the command, if it could not be executed"},
			"omitted_bytes": {Type: genai.TypeInteger, Description: "Bytes cut from the middle of a large output, 0 when the output is complete"},
		},
	}
}

// commandTimeout is the error of a command that timed out.
type commandTimeout int

func (s commandTimeout) Error() string { return fmt.Sprintf("command timed out after %ds", int(s)) }

// readbackTimeout bounds the exec that reads back a timed-out call's output.
const readbackTimeout = 30

// detachOutput sends the command's output to the file out and replays it once
// the command's own shell exits, so a process left in the background (e.g.
// timewarp) won't hold the call's pipe open. The file is removed before the
// replay, so only a call killed earlier leaves it for readbackScript.
func detachOutput(command, out string) string {
	return "( " + command + "\n) >" + out + " 2>&1; rc=$?; { rm -f " + out + "; cat; } <" + out + "; exit $rc"
}

// readbackScript replays and removes a timed-out call's output file. It first
// kills whatever still writes to the file, which a timeout misses inside a
// container.
func readbackScript(out string) string {
	return "for p in /proc/[0-9]*; do { [ $p/fd/1 -ef " + out + " ] || [ $p/fd/2 -ef " + out + " ]; } && kill -9 ${p#/proc/}; done 2>/dev/null; cat " + out + " 2>/dev/null; rm -f " + out
}

// dockerExecInContainerScript wraps command to run inside the retained build
// container. prepareBuild leaves exactly one rb-* container between builds
// which is used here by starting (if stopped) and exec'ing the command there
// so the in-container exit code propagates. Exits 125 when no build has run.
func dockerExecInContainerScript(command string) string {
	return `c=$(docker ps -aq --filter name=^rb- | head -1); ` +
		`[ -n "$c" ] || { echo 'no build container found; run a build first' >&2; exit 125; }; ` +
		`docker start "$c" >/dev/null; ` +
		`exec docker exec -w /src "$c" /bin/sh -c ` + posixSingleQuote(command)
}

// posixSingleQuote renders s as a single-quoted POSIX sh word, escaping any
// embedded single quotes.
func posixSingleQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

func (a *defaultAgent) proposeInferenceWithAIAssist(ctx context.Context, initialErr error, wt billy.Filesystem, str storage.Storer) (*schema.StrategyOneOf, error) {
	prompt := []string{
		fmt.Sprintf("Based on the following inference failure error \"%v\" for package '%s', find the correct source code repository URL.", initialErr, a.t.Package),
		"Just return the URL WITHOUT any additional text or formatting.",
		"For example, for the package 'org.apache.camel:camel-support', return 'https://github.com/apache/camel' not 'https://github.com/apache/camel/tree/main/core/camel-support'.",
		"Use the tools you have at your disposal to find the URL.",
		"Finally, if you don't find the URL, just return an empty string.",
	}
	var repoURL string
	err := a.deps.Retrier.Do(ctx, func() error {
		url, usage, err := llm.GenerateTextContentWithUsage(ctx, a.deps.GenaiClient, cmp.Or(a.deps.Model, llm.GeminiPro), &genai.GenerateContentConfig{
			Temperature: new(float32(0.0)),
			Tools: []*genai.Tool{
				{GoogleSearch: &genai.GoogleSearch{}},
			},
		}, genai.NewPartFromText(strings.Join(prompt, "\n")))
		a.addSideUsage(usage)
		repoURL = url
		return err
	})
	if err != nil {
		return nil, errors.Wrap(err, "getting AI repo hint")
	}
	if repoURL == "" {
		return nil, errors.Wrap(initialErr, "AI could not find a repository hint")
	}
	log.Printf("AI suggested repo hint: %s", repoURL)
	req := schema.InferenceRequest{
		Ecosystem: a.t.Ecosystem,
		Package:   a.t.Package,
		Version:   a.t.Version,
		Artifact:  a.t.Artifact,
		StrategyHint: &schema.StrategyOneOf{
			LocationHint: &rebuild.LocationHint{
				Location: rebuild.Location{
					Repo: repoURL,
				},
			},
		},
	}
	s, err := inferenceservice.Infer(
		ctx,
		req,
		&inferenceservice.InferDeps{
			HTTPClient:         a.deps.RegistryClient,
			GitCache:           a.deps.GitCache,
			CratesRegistryStub: a.deps.CratesRegistryStub,
			CratesCache:        a.deps.CratesCache,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{
					Worktree: wt,
					Storer:   str,
				}
			},
		},
	)
	return s, errors.Wrap(err, "AI-assisted inference failed")
}

// toolchainSteps install an ecosystem's toolchain for a first attempt that
// only checks out the source. Later proposals inherit the deps unchanged and
// change versions from the build script.
var toolchainSteps = map[rebuild.Ecosystem][]flow.Step{
	rebuild.NPM:      {{Uses: "npm/install-node", With: map[string]string{"nodeVersion": "24.20.0"}}},
	rebuild.PyPI:     {{Uses: "pypi/setup-venv", With: map[string]string{"locator": "/usr/bin/", "path": "/deps", "pythonVersion": ""}}},
	rebuild.CratesIO: {{Uses: "cargo/deps/toolchain", With: map[string]string{"rustVersion": "1.97.0"}}},
}

// registrySteps pin the package manager to the timewarp registry at the
// publish time. The pin opens the build script, which the model edits rather
// than replaces, so it carries into later proposals. Cargo pins a registry
// index commit instead, which inference resolves from a lockfile.
var registrySteps = map[rebuild.Ecosystem]string{
	rebuild.NPM:  "npm/setup-registry",
	rebuild.PyPI: "pypi/setup-registry",
}

// toolchainStrategy checks out loc and installs the ecosystem toolchain. The
// build script holds only the registry pin, so the attempt fails at the
// artifact and leaves the source and toolchain in the retained container.
func toolchainStrategy(eco rebuild.Ecosystem, loc rebuild.Location, published time.Time) rebuild.Strategy {
	s := &rebuild.WorkflowStrategy{
		Location:  loc,
		Source:    []flow.Step{{Uses: "git-checkout"}},
		Deps:      toolchainSteps[eco],
		OutputDir: loc.Dir,
	}
	if step, ok := registrySteps[eco]; ok && !published.IsZero() {
		s.Build = []flow.Step{{Uses: step, With: map[string]string{"registryTime": published.Format(time.RFC3339)}}}
	}
	return s
}

// toolchainFallback turns an inference failure that resolved the source into
// a toolchain-only first attempt, which leaves a container and logs for the
// model to diagnose. Any other failure is returned as is.
func (a *defaultAgent) toolchainFallback(err error) (*schema.StrategyOneOf, error) {
	detail, ok := api.DetailOf[rebuild.InferenceErrorDetail](err)
	if !ok {
		return nil, err
	}
	log.Printf("Inference stopped short of a build: %v", err)
	a.inferenceFailure = status.Convert(err).Message() // without the "rpc error" framing
	return new(schema.NewStrategyOneOf(toolchainStrategy(a.t.Ecosystem, detail.Location, detail.Published))), nil
}

func (a *defaultAgent) proposeHeuristicInference(ctx context.Context) (*schema.StrategyOneOf, error) {
	wt := memfs.New()
	str := gitx.NewInMemoryStorer()
	s, err := inferenceservice.Infer(
		ctx,
		schema.InferenceRequest{
			Ecosystem: a.t.Ecosystem,
			Package:   a.t.Package,
			Version:   a.t.Version,
			Artifact:  a.t.Artifact,
		},
		&inferenceservice.InferDeps{
			HTTPClient:         a.deps.RegistryClient,
			GitCache:           a.deps.GitCache,
			CratesRegistryStub: a.deps.CratesRegistryStub,
			CratesCache:        a.deps.CratesCache,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{
					Worktree: wt,
					Storer:   str,
				}
			},
		},
	)
	var tooLarge *gitx.RepoTooLargeError
	if errors.As(err, &tooLarge) {
		// The repository was located, so a model repository hint cannot help.
		return nil, err
	}
	// A failure that resolved the source has its repository, so a model
	// repository hint cannot help it.
	if _, located := api.DetailOf[rebuild.InferenceErrorDetail](err); err != nil && !located {
		wt, str = memfs.New(), gitx.NewInMemoryStorer()
		if s, err = a.proposeInferenceWithAIAssist(ctx, err, wt, str); err == nil {
			log.Println("AI-assisted inference succeeded.")
		}
	}
	if err != nil {
		if s, err = a.toolchainFallback(err); err != nil {
			return nil, errors.Wrap(err, "AI-assisted inference failed")
		}
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
		"You are an open source build expert. You will work towards getting the target package to build successfully and match the upstream.",
		fmt.Sprintf("You are attempting to rebuild %#v", a.t),
		"Every byte of the artifact must be produced by the package's own build tooling from the source at the ref, or be absent. Never author, copy, or transcribe artifact content into the source tree or the output, and never edit or repack the archive after the packaging tool produces it. A build that matches only because you wrote the differing bytes is a failure, not a success: an honest mismatch is the better outcome.",
	}
}

func (a *defaultAgent) diagnoseOnly() []string {
	prompt := []string{
		"Please explain what went wrong with the rebuild, and what might need to be changed to resolve the build.",
		"You can include in-line code snippets, but the overall description should only be two or three sentences.",
		"To debug the build, you might want to use the read_logs_end tool to view the build errors to understand what's going wrong.",
		"You might also want to inspect the contents of the source repo using the read_repo_file or list_repo_files tools.",
	}
	if a.singleEnv() {
		prompt = append(prompt,
			"You can use the run_in_container tool to run commands inside the retained build container with the build's own toolchain. Everything is in the container.",
			"Where things are, all in the container: the source checkout is at /src, where each call starts; your rebuild is at /out/rebuild; diffr is at "+scratch.ContainerDiffrPath()+" and the upstream artifact is under "+scratch.ContainerUpstreamDir+"; and "+scratch.ContainerBuildsDir+" holds this and every earlier attempt's build.sh, out/, and src/.",
			fmt.Sprintf("Read the build log with read_logs_end before exploring, keep tool output small with head, tail, grep, and wc rather than printing whole files or trees (a result keeps only its first %dKB and last %dKB), and aim to reach a diagnosis in under 20 tool calls. The exchange ends after %d.", cmp.Or(a.deps.ShellHeadBytes, toolOutputHead)/1000, cmp.Or(a.deps.ShellTailBytes, toolOutputTail)/1000, cmp.Or(a.deps.MaxTurns, maxToolTurns)),
		)
	} else if a.deps.Shell != nil {
		prompt = append(prompt,
			"You can also use the run_on_host tool to run diagnostic shell commands on the build VM host (e.g. inspect docker images or examine files left by previous build attempts), and the run_in_container tool to run commands inside the retained build container with the build's own toolchain.",
			"Where things are: the source checkout is at /src inside the retained container, where each call starts, and that same tree is the host directory ./builds/<build-id>/src, so a file you produce in the container is readable by host tools like diffr at that path. Host tools cannot read other container paths. The previous attempt's build script and output are under ./builds/<build-id>/ on the host. After a content mismatch the error message names where the upstream artifact and diffr sit on the host.",
			fmt.Sprintf("Read the build log with read_logs_end before exploring, keep tool output small with head, tail, grep, and wc rather than printing whole files or trees (a result keeps only its first %dKB and last %dKB), and aim to reach a diagnosis in under 20 tool calls. The exchange ends after %d.", cmp.Or(a.deps.ShellHeadBytes, toolOutputHead)/1000, cmp.Or(a.deps.ShellTailBytes, toolOutputTail)/1000, cmp.Or(a.deps.MaxTurns, maxToolTurns)),
		)
	}
	return append(prompt,
		fmt.Sprintf("The source is %s at %s. If the evidence says the release was cut from a different commit (the published gitHead or .cargo_vcs_info.json sha, a tag, a version-bump commit), call set_source_ref with that commit now; the next attempt is inferred and built from it. Do not plan a checkout in the build script.", a.loc.Repo, a.loc.Ref),
		"Another LLM will use your diagnosis to propose a fix.",
	)
}

func (a *defaultAgent) outputOnlyScript(outputPath string) []string {
	return []string{
		"When responding, please only respond with the bash script. Do not include any english text before or after the bash script, and do not include any formatting.",
		"You should include your reasoning as comments inside the bash script.",
		"These comments should sufficiently explain to a future reader how you got to this bash script compared to the original script.",
		"DO NOT include markdown backtics, or ANY formatting besides the raw content of the bash script.",
		"You SHOULD NOT change the repo in the location. Even if you do, that will be overwritten when we attempt to execute the build again.",
		"You SHOULD NOT check out another commit in the script. If the release was cut from a different commit than the ref you were given, that is a diagnosis: call set_source_ref with the commit and the evidence, and the next attempt clones and installs from it. A script that moves the source checkout to another revision, or takes files from one, is refused.",
		"Write gitHead and similar commit fields from git rev-parse HEAD, never as a typed sha: at the right ref they are the same value, and at the wrong ref the typed one hides that the ref is wrong.",
		"Forbidden in the script, and a reviewer rejects the build:",
		"- writing literal content (heredoc, printf, echo, tee, node -e, python -c, a typed hash, date, or version) into any file that ships in the artifact, lockfiles and package metadata included",
		"- downloading or extracting anything from the published artifact or from any commit, branch, or repository other than the ref",
		"- unpacking, editing, or re-creating the output archive with tar, zip, wheel, zipfile, or similar instead of letting the build tool produce it",
		"- editing the content of shipped source, metadata, or documentation files",
		"- deleting shipped files so the file set matches",
		"Allowed, with a comment saying why:",
		"- reproducing the publisher's environment (tool versions, environment variables, a build tool's configuration file wherever it lives, under node_modules or another dependency or tool directory included, removing .git so version tooling behaves as it did for the publisher, a value the publisher's tooling derives from the source such as gitHead from git rev-parse HEAD, a checkout with the line endings the publisher had)",
		"- deleting files your build produces that upstream does not ship",
		"- editing build inputs that never ship",
		"- edits that leave the shipped bytes identical",
		"If upstream contains content that cannot come from the source at the ref, such as a stray coverage or cache directory, editor swap files, or an untracked build product, say so in a comment and leave the mismatch. Do not recreate it.",
		"To debug the build, you might want to use the read_logs_end tool to view the build errors to understand what's going wrong.",
		"You might also want to inspect the contents of the source repo using the read_repo_file or list_repo_files tools.",
		fmt.Sprintf("Make sure artifact eventually ends up in %s, which is the path from which it will be exported.", outputPath),
		"DO NOT create or modify the /out directory. That will be done for you after the build script finishes.",
	}
}

func (a *defaultAgent) ecosystemExpertise() []string {
	// TODO: Actually implement this
	switch a.t.Ecosystem {
	case rebuild.Maven:
		return []string{}
	case rebuild.Debian:
		return []string{}
	case rebuild.OCI:
		return []string{}
	default:
		return []string{}
	}
}

// executionDetails about the previous execution
type executionDetails struct {
	Iteration    schema.AgentIteration
	Instructions rebuild.Instructions
	Dockerfile   string
}

func (a *defaultAgent) dockerfile(ctx context.Context, obliviousID string) (string, error) {
	if obliviousID == "" {
		return "", errors.New("no oblivious ID provided")
	}
	meta, err := rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, obliviousID), a.deps.GCSClient, fmt.Sprintf("gs://%s", a.deps.MetadataBucket))
	if err != nil {
		return "", err
	}
	r, err := meta.Reader(ctx, rebuild.DockerfileAsset.For(a.t))
	if err != nil {
		return "", errors.Wrap(err, "opening dockerfile")
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "reading dockerfile")
	}
	return string(data), nil
}

func (a *defaultAgent) execDetails(ctx context.Context, iteration *schema.AgentIteration) *executionDetails {
	d := &executionDetails{Iteration: *iteration}
	if iteration.Strategy == nil {
		return d
	}
	s, err := iteration.Strategy.Strategy()
	if err != nil {
		return d
	}
	{ // Instructions - generated from strategy
		// NOTE: These instructions might differ slightly from the ones in the dockerfile that was used for the build.
		// We do this because GenerateFor is the most straightforward way of separating the source, deps, and build steps.
		inst, err := s.GenerateFor(a.t, a.buildEnv())
		if err != nil {
			log.Println(errors.Wrap(err, "generating instructions"))
		} else {
			d.Instructions = inst
		}
	}
	{ // Dockerfile - fetched from infrastructure
		d.Dockerfile, err = a.dockerfile(ctx, iteration.ObliviousID)
		if err != nil {
			log.Println(errors.Wrap(err, "getting dockerfile"))
		}
	}
	return d
}

// buildEnv is the env the previous attempt's strategy is rendered for. The
// next proposal inherits the rendered Deps verbatim and runs them on the
// scratch VM and then on GCB, so timewarp must be configured the same.
func (a *defaultAgent) buildEnv() rebuild.BuildEnv {
	if a.deps.BuildEnv.TimewarpHost != "" {
		return a.deps.BuildEnv
	}
	return rebuild.BuildEnv{TimewarpHost: "localhost:8080", HasRepo: false}
}

func (a *defaultAgent) historyContext(prev *executionDetails) []string {
	prompt := []string{
		"# History",
		"Here are the details of the previous attempt.",
		"You can only control the build script, but other details are included to help you diagnose the failure.",
	}
	if prev != nil {
		prompt = append(prompt,
			"## Build script:",
			"",
			"This is the content you can control, focus on and update",
			"```bash",
			prev.Instructions.Build,
			"```",
			"",
			"## Error message:",
			"```",
			prev.Iteration.Result.ErrorMessage,
			"```",
		)
		if prev.Dockerfile != "" {
			prompt = append(
				prompt,
				"",
				"## Dockerfile",
				"This is for debugging purposes only. Do not include this file's contents in your response):",
				"",
				"```dockerfile",
				prev.Dockerfile,
				"```",
				"",
			)
		}
	}
	if a.inferenceFailure != "" {
		prompt = append(prompt,
			"",
			"## Inference",
			"Heuristic inference resolved the source but could not produce a build, so the first attempt only checked out the source and installed the toolchain. The inference failure was:",
			"```",
			a.inferenceFailure,
			"```",
		)
	}
	if len(a.refMoves) > 0 {
		prompt = append(prompt, "", "## Source ref")
		for _, m := range a.refMoves {
			prompt = append(prompt, fmt.Sprintf("- moved from %s to %s: %s", m.From, m.To, m.Reason))
		}
		prompt = append(prompt, fmt.Sprintf("The source is now %s at %s. The deps phase was re-inferred there.", a.loc.Repo, a.loc.Ref))
	}
	if len(a.refusals) > 0 {
		prompt = append(prompt,
			"",
			"## Refused proposals",
			"These commands were refused because they supply artifact content instead of building it. Do not propose them again in any form.",
		)
		for _, v := range a.refusals {
			prompt = append(prompt, fmt.Sprintf("- %s %s: %s (%s)", v.Rule, buildpolicy.Title(v.Rule), v.Line, v.Why))
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

func (a *defaultAgent) generate(ctx context.Context, prompt []string, opts *ProposeOpts) (string, error) {
	var response genai.Content
	contentParts := []*genai.Part{genai.NewPartFromText(strings.Join(prompt, "\n"))}
	var partNum int
	// Use timestamp for each call to generate to avoid collisions
	invokeTime := time.Now().UTC().Format(time.RFC3339Nano)
	for content, err := range a.deps.Chat.SendMessageStream(ctx, contentParts...) {
		if err != nil {
			return "", errors.Wrap(err, "chat error")
		}
		log.Printf("%s\n\n", llm.FormatContent(*content))
		if opts.ChatUploadURL != nil {
			contentPath := path.Join(opts.ChatUploadURL.Path, fmt.Sprintf("%s-%d-%s.json", invokeTime, partNum, content.Role))
			var writer io.WriteCloser
			switch opts.ChatUploadURL.Scheme {
			case "gs":
				writer = a.deps.GCSClient.Bucket(opts.ChatUploadURL.Host).Object(contentPath).NewWriter(ctx)
			// TODO: implement local fs storage
			default:
				return "", fmt.Errorf("unsupported chat upload scheme: \"%s\"", opts.ChatUploadURL.Scheme)
			}
			defer writer.Close()
			enc := json.NewEncoder(writer)
			enc.SetIndent("", "  ")
			if err := enc.Encode(content); err != nil {
				return "", errors.Wrap(err, "writing to bucket")
			}
		}
		response = *content
		partNum++
	}
	return textOf(response.Parts), nil
}

// textOf joins a model turn's text parts, skipping thoughts. A long script can
// arrive split across parts.
func textOf(parts []*genai.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p != nil && p.Text != "" && !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// One "cycle" of the LLM produces a thoughtData
type thoughtData struct {
	BasedOnIteration int    // The index in iterationHistory on which this thought is based.
	Diagnostic       string // The reasoning of why things were broken, and what might need to be fixed.
	UpdatedScript    string // The updated script.
}

func (a *defaultAgent) proposeAgentInference(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	if len(a.iterHistory) == 0 {
		return nil, errors.New("proposeAgentInferece needs an previous iteration to work off of")
	}
	// Use a fresh chat per iteration. The history carries prior thoughts but
	// the content of tool calls and results across iterations accumulates too
	// fast and overflows the model's input.
	if a.deps.ChatFn != nil {
		if a.deps.Chat != nil {
			a.sideUsage = a.sideUsage.Add(sumTokenUsage(a.deps.Chat.Usage(), a.deps.Chat.Model()))
		}
		chat, err := a.deps.ChatFn(ctx, a.getTools())
		if err != nil {
			return nil, errors.Wrap(err, "starting chat")
		}
		a.deps.Chat = chat
	}
	prev := a.execDetails(ctx, a.iterHistory[len(a.iterHistory)-1])
	thought := thoughtData{
		BasedOnIteration: len(a.iterHistory) - 1,
	}
	// We have a dedicated diagnostic step to make sure we keep a history of what the AI thinks the problems are.
	{ // Diagnose
		log.Println("Asking the LLM to diagnose the failure and describe a fix...")
		prompt := a.genericPrompt()
		prompt = append(prompt, a.ecosystemExpertise()...)
		prompt = append(prompt, a.historyContext(prev)...)
		prompt = append(prompt, a.diagnoseOnly()...)
		var err error
		thought.Diagnostic, err = a.generate(ctx, prompt, opts)
		if err != nil {
			return nil, errors.Wrap(err, "diagnose")
		}
	}
	a.reinferAfterMove(ctx, opts, &prev.Instructions)
	var rawScript string
	{ // Implement
		log.Println("Asking the LLM to hypothesize a fix")
		prompt := a.genericPrompt()
		prompt = append(prompt, a.ecosystemExpertise()...)
		prompt = append(prompt, a.outputOnlyScript(prev.Instructions.OutputPath)...)
		prompt = append(prompt, "An expert reviewed this failure and gave these instructions for fixing it:", thought.Diagnostic)
		// TODO: Switch the prompt to outputReasoningAndScript for structured reasoning.
		var err error
		rawScript, err = a.generate(ctx, prompt, opts)
		if err != nil {
			return nil, errors.Wrap(err, "hypothesize")
		}
	}
	{ // Clean the script
		// TODO: Change this to use gemini flash instead
		script, err := a.generate(
			ctx,
			[]string{
				"You are now a single-purpose, pure Bash script generator.",
				"Your entire output must be a single, raw, ready-to-execute Bash script.",
				"You must not include any surrounding text, explanations, markdown formatting (like ```bash or ```), titles, or conversational filler.",
				"Start your response immediately with the first line of the Bash script.",
				"From the following llm response, extract only the shell commands to be run and exclude any commands used to clone, checkout, and navigate to the git repo:",
				rawScript,
			},
			opts,
		)
		if err != nil {
			return nil, errors.Wrap(err, "ai formatting")
		}
		// TODO: Check the script for invalid sequences, like EOS or EOF.
		if strings.HasPrefix(script, "```") {
			lines := strings.Split(script, "\n")
			lines = lines[1:]
			script = strings.Join(lines, "\n")
		}
		script = strings.Replace(script, "```", "", -1)
		thought.UpdatedScript = script
	}
	a.thoughts = append(a.thoughts, thought)
	// TODO: Try to format the bash script into a structured strategy?
	return new(schema.NewStrategyOneOf(&rebuild.ManualStrategy{
		Location:   a.loc,
		Requires:   prev.Instructions.Requires,
		Deps:       prev.Instructions.Deps,
		Build:      thought.UpdatedScript,
		OutputPath: prev.Instructions.OutputPath,
	})), nil
}

func (a *defaultAgent) Propose(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	var s *schema.StrategyOneOf
	var err error
	record := "0-proposal.json"
	if len(a.iterHistory) == 0 {
		// For the first iteration, use our regular inference logic. It walks git
		// history without consulting its context, so the call is abandoned rather
		// than interrupted.
		// NOTE: An abandoned call still sets a.repo, a.loc and a.inferenceFailure.
		ictx, cancel := context.WithTimeoutCause(ctx, inferenceTimeout, errors.Wrapf(context.DeadlineExceeded, "inference ran past %s", inferenceTimeout))
		defer cancel()
		type proposal struct {
			s   *schema.StrategyOneOf
			err error
		}
		ch := make(chan proposal, 1) // buffered so an abandoned call's send never blocks
		go func() {
			s, err := withPanicRecovered(ictx, a.proposeHeuristicInference)
			ch <- proposal{s, err}
		}()
		select {
		case p := <-ch:
			s, err = p.s, p.err
		case <-ictx.Done():
			return nil, context.Cause(ictx)
		}
		record = "0-inference-proposal.json"
	} else if a.ff != nil {
		s, err = a.ff.propose(ctx, opts)
	} else {
		s, err = a.proposeAgentInference(ctx, opts)
	}
	if err == nil {
		// The strategy the executor runs is otherwise recoverable only from
		// model prose, and the heuristic proposal has no chat at all.
		a.uploadProposalRecord(ctx, opts, record, s)
	}
	return s, err
}

// Canonicalize rewrites a build that already reproduces upstream toward the
// form inference produces, with registry access confined to the deps phase.
// The GCB confirmation runs the phases in isolation, so a registry call in
// the build phase fails there though the combined-shell scratch build
// tolerates it.
func (a *defaultAgent) Canonicalize(ctx context.Context, opts *ProposeOpts, working *schema.StrategyOneOf) (out *schema.StrategyOneOf, changed bool, err error) {
	s, err := working.Strategy()
	if err != nil {
		return working, false, errors.Wrap(err, "reading working strategy")
	}
	ms, ok := s.(*rebuild.ManualStrategy)
	if !ok {
		return working, false, nil // already a typed, inference-shaped strategy
	}
	// Registry access goes through the timewarp host.
	marker := a.buildEnv().TimewarpHost
	if marker == "" || !strings.Contains(ms.Build, marker) {
		return working, false, nil // no registry access in the build phase
	}
	// A fresh chat, as each proposal gets: fold the diagnose chat's tokens
	// into the side total first so they are not lost when it is replaced.
	if a.deps.ChatFn != nil {
		if a.deps.Chat != nil {
			a.sideUsage = a.sideUsage.Add(sumTokenUsage(a.deps.Chat.Usage(), a.deps.Chat.Model()))
		}
		chat, err := a.deps.ChatFn(ctx, nil)
		if err != nil {
			return working, false, errors.Wrap(err, "starting canonicalize chat")
		}
		a.deps.Chat = chat
	}
	prompt := []string{
		"You are refining a build definition that already reproduces the upstream artifact byte-for-byte.",
		"It runs as two sequential phases. The deps phase has registry and network access. The build phase does not: it only compiles what deps fetched.",
		"Rewrite the two phases so that every registry or network access happens in the deps phase and none remains in the build phase. Do not change what is built or the produced artifact. It must still reproduce upstream.",
		"Preserve execution order. The phases run in sequence, deps then build, as one command sequence, and the order of installs and setup steps is often load-bearing. A command you move into the deps phase must keep its position relative to the others: append it after the existing deps commands, never before them, and never reorder one install ahead of another.",
		"Current deps phase:", ms.Deps,
		"Current build phase:", ms.Build,
		"Output the new deps phase, then a line containing only ===BUILD===, then the new build phase. Emit only the two scripts: no markdown fences, no commentary.",
	}
	raw, err := a.generate(ctx, prompt, opts)
	if err != nil {
		return working, false, errors.Wrap(err, "canonicalize generate")
	}
	deps, build, ok := splitCanonical(raw)
	if !ok {
		return working, false, errors.New("canonicalize output missing ===BUILD=== separator")
	}
	return new(schema.NewStrategyOneOf(&rebuild.ManualStrategy{
		Location:   ms.Location,
		Requires:   ms.Requires,
		Deps:       deps,
		Build:      build,
		OutputPath: ms.OutputPath,
	})), true, nil
}

// splitCanonical parses Canonicalize's two-script output on the ===BUILD===
// separator, stripping any code fences the model added around each script.
func splitCanonical(out string) (deps, build string, ok bool) {
	const sep = "===BUILD==="
	i := strings.Index(out, sep)
	if i < 0 {
		return "", "", false
	}
	return stripFences(out[:i]), stripFences(out[i+len(sep):]), true
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"```bash", "```shell", "```sh", "```json", "```"} {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			break
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// uploadProposalRecord records an iteration's proposed strategy in its
// transcript directory. Best effort: a failed upload doesn't fail the proposal.
func (a *defaultAgent) uploadProposalRecord(ctx context.Context, opts *ProposeOpts, obj string, s *schema.StrategyOneOf) {
	a.uploadRecord(ctx, opts, obj, s)
}

// uploadRecord writes v to the iteration's transcript directory, best effort.
func (a *defaultAgent) uploadRecord(ctx context.Context, opts *ProposeOpts, obj string, v any) {
	if opts == nil || opts.ChatUploadURL == nil || opts.ChatUploadURL.Scheme != "gs" || a.deps.GCSClient == nil {
		return
	}
	uploadJSONRecord(ctx, a.deps.GCSClient, opts.ChatUploadURL.Host, path.Join(opts.ChatUploadURL.Path, obj), v)
}

// uploadJSONRecord writes v as JSON to obj, logging failures.
func uploadJSONRecord(ctx context.Context, client *gcs.Client, bucket, obj string, v any) {
	w := client.Bucket(bucket).Object(obj).NewWriter(ctx)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("session transcript: writing %s: %v", path.Base(obj), err)
	}
	if err := w.Close(); err != nil {
		log.Printf("session transcript: closing %s: %v", path.Base(obj), err)
	}
}

func (a *defaultAgent) RecordIteration(i *schema.AgentIteration) {
	a.iterHistory = append(a.iterHistory, i)
}

// Check reviews a proposal whose scratch build matched upstream, before it
// is rebuilt on GCB. A refusal's violations stay in every later prompt.
func (a *defaultAgent) Check(ctx context.Context, opts *ProposeOpts, proposal *schema.StrategyOneOf) (string, error) {
	if a.ff != nil {
		return a.ff.check(ctx, opts, proposal)
	}
	return a.judgeProposal(ctx, opts, proposal)
}

// judgeProposal is the judgment Check makes of a manual strategy's shell.
func (a *defaultAgent) judgeProposal(ctx context.Context, opts *ProposeOpts, proposal *schema.StrategyOneOf) (string, error) {
	st, err := proposal.Strategy()
	if err != nil {
		return "", errors.Wrap(err, "reading proposal")
	}
	ms, ok := st.(*rebuild.ManualStrategy)
	if !ok {
		return "", nil // typed strategies have no shell
	}
	in := a.reviewInput(ms.Ref)
	in.Deps, in.Build = ms.Deps, ms.Build
	return a.judge(ctx, in, func(v *buildpolicy.Review) {
		a.uploadRecord(ctx, opts, time.Now().UTC().Format(time.RFC3339Nano)+"-review.json", v)
	})
}

// reviewInput holds the target and the attempt a proposal revises.
func (a *defaultAgent) reviewInput(ref string) buildpolicy.ReviewInput {
	in := buildpolicy.ReviewInput{
		Target:    buildpolicy.Target{Package: a.t.Package, Version: a.t.Version, Ref: ref},
		Ecosystem: string(a.t.Ecosystem),
		Artifact:  a.t.Artifact,
	}
	if n := len(a.iterHistory); n > 0 {
		in.PreviousBuild, in.PreviousError = a.previousAttempt(a.iterHistory[n-1])
	}
	return in
}

// judge returns the reviewer's refusal, if any, and passes the verdict to
// record. Without a reviewer it allows.
func (a *defaultAgent) judge(ctx context.Context, in buildpolicy.ReviewInput, record func(*buildpolicy.Review)) (string, error) {
	if a.reviewer == nil {
		return "", nil
	}
	var verdict *buildpolicy.Review
	err := a.deps.Retrier.Do(ctx, func() error {
		r, usage, err := a.reviewer(ctx, in)
		a.addSideUsage(usage)
		if err != nil {
			return err
		}
		verdict = r
		return nil
	})
	if err != nil {
		return "", errors.Wrap(err, "reviewing proposal")
	}
	record(verdict)
	if verdict.Rejected() {
		a.refusals = append(a.refusals, verdict.Violations...)
		return verdict.Message(), nil
	}
	return "", nil
}

// reviewErrorBytes caps the previous error shown to the reviewer. The head of
// a long diff summary carries the file list.
const reviewErrorBytes = 8000

// previousAttempt returns prev's build script and error for the reviewer.
func (a *defaultAgent) previousAttempt(prev *schema.AgentIteration) (build, errMsg string) {
	if prev.Strategy != nil {
		if st, err := prev.Strategy.Strategy(); err == nil {
			if inst, err := st.GenerateFor(a.t, a.buildEnv()); err == nil {
				build = inst.Build
			} else if ms, ok := st.(*rebuild.ManualStrategy); ok {
				build = ms.Build
			}
		}
	}
	if prev.Result != nil {
		errMsg = prev.Result.ErrorMessage
		if len(errMsg) > reviewErrorBytes {
			errMsg = errMsg[:reviewErrorBytes] + "\n[cut]"
		}
	}
	return build, errMsg
}

// addSideUsage attributes the token usage of an LLM call made outside the main
// chat to the agent's running total. The label must match the model the side
// call actually used (the session's model, same as the chat), or summing it
// with the chat's usage trips TokenUsage.Add's cross-model guard and crashes
// the session.
func (a *defaultAgent) addSideUsage(m *genai.GenerateContentResponseUsageMetadata) {
	if m == nil {
		return
	}
	a.sideUsage = a.sideUsage.Add(tokenUsageFromMetadata(m, cmp.Or(a.deps.Model, llm.GeminiPro)))
}

// Usage returns the agent's cumulative LLM token consumption: the main chat's
// tally plus side calls made outside it.
func (a *defaultAgent) Usage() schema.TokenUsage {
	u := a.sideUsage
	if a.deps != nil && a.deps.Chat != nil {
		u = u.Add(sumTokenUsage(a.deps.Chat.Usage(), a.deps.Chat.Model()))
	}
	return u
}

// withPanicRecovered returns a panic in infer as an error. The stack goes to
// the log, not the error text, which llm.IsTransient matches by substring.
func withPanicRecovered(ctx context.Context, infer func(context.Context) (*schema.StrategyOneOf, error)) (s *schema.StrategyOneOf, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Inference panicked: %v\n%s", r, debug.Stack())
			s, err = nil, errors.Errorf("inference panicked: %v", r)
		}
	}()
	return infer(ctx)
}
