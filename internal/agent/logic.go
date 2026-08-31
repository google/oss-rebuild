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
	"net/http"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

const uploadBytesLimit = 100_000

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
	assets      rebuild.AssetStore
	gitTools    []*llm.FunctionDefinition
	sideUsage   schema.TokenUsage // accumulates LLM token consumption from calls made outside the main chat
}

func NewDefaultAgent(t rebuild.Target, deps *AgentDeps) *defaultAgent {
	a := &defaultAgent{t: t, deps: deps, assets: rebuild.NewFilesystemAssetStore(memfs.New())}
	a.gitTools = llm.GitTools(func() (*git.Repository, string) {
		return a.repo, a.loc.Ref
	})
	return a
}

func (a *defaultAgent) InitializeFromIteration(ctx context.Context, initialIteration *schema.AgentIteration) error {
	loc, err := locationFromStrategyOneOf(initialIteration.Strategy)
	if err != nil {
		return errors.Wrap(err, "parsing previous iteration")
	}
	repo, err := rebuild.LoadRepo(ctx, a.t.Package, memory.NewStorage(), memfs.New(), git.CloneOptions{URL: loc.Repo, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	if err != nil {
		return errors.Wrap(err, "loading repo")
	}
	a.repo = repo.Repository
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

func (a *defaultAgent) getTools() []*llm.FunctionDefinition {
	tools := append(a.gitTools, a.readLogsTool())
	if a.deps.ScratchRunner != nil {
		tools = append(tools, a.runOnHostTool(), a.runInContainerTool())
	}
	return tools
}

func (a *defaultAgent) readLogsTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "read_logs_end",
			Description: "Read tail of the logs from the previous build. If the logs are large, they may be truncated providing only the tail.",
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
		Function: func(args map[string]any) genai.FunctionResponse {
			logs, err := a.readPreviousBuildLogs(context.Background())
			if err != nil {
				return genai.FunctionResponse{
					Name: "read_logs_end", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"logs":  "",
						"error": err.Error(),
					},
				}
			}
			if len(logs) > uploadBytesLimit {
				logs = "...(truncated)..." + logs[len(logs)-uploadBytesLimit:]
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
	if a.deps.ScratchRunner != nil {
		logs, err := a.deps.ScratchRunner.LastBuildLogs(ctx)
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
			Description: "Run a shell command on the build VM host (interpreted by /bin/sh -c). The host is minimal: docker and coreutils only, with no language runtimes or fetch tools. Use run_in_container for anything needing the build environment. Use the host to inspect docker images and containers and to retain or analyze files from previous build attempts e.g. ./builds/<build-id>/ containing the attempt's build.sh and out/ directory. The most recent build's container is retained as rb-<build-id> and can be identified with `docker ps -a --filter name=rb-` but all prior build containers are removed once the next build is triggered. Returns the merged stdout/stderr (truncated to the tail if large) and the exit code.",
			Parameters:  shellCommandParams(),
			Response:    shellCommandResponse(),
		},
		Function: func(args map[string]any) genai.FunctionResponse {
			return a.runShell("run_on_host", args, nil)
		},
	}
}

func (a *defaultAgent) runInContainerTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "run_in_container",
			Description: "Run a shell command inside the retained build container (rb-<build-id>), which carries the build's toolchain, source tree, and output from the most recent attempt. Use it to analyze the build environment, inspect intermediate state, or test a fix in the real build environment before proposing a new build. Each call is an independent 'bin/sh -c' so working directory and shell environment will carry across calls while filesystem changes will not. Requires a prior build attempt (exits 125 otherwise). Returns the merged stdout/stderr (truncated to the tail if large) and the exit code.",
			Parameters:  shellCommandParams(),
			Response:    shellCommandResponse(),
		},
		Function: func(args map[string]any) genai.FunctionResponse {
			return a.runShell("run_in_container", args, dockerExecInContainerScript)
		},
	}
}

// runShell dispatches a shell command to the scratch VM for tool `name`. When
// wrap is non-nil the raw command is transformed before dispatch (e.g. to exec
// it inside the build container). It normalizes the timeout, truncates large
// output to its tail, and shapes the response the LLM expects.
func (a *defaultAgent) runShell(name string, args map[string]any, wrap func(string) string) genai.FunctionResponse {
	command, _ := args["command"].(string)
	if command == "" {
		return shellResponse(name, "", 0, "command is required")
	}
	timeout, terr := shellTimeout(args)
	if terr != "" {
		return shellResponse(name, "", 0, terr)
	}
	script := command
	if wrap != nil {
		script = wrap(command)
	}
	exitCode, output, err := a.deps.ScratchRunner.RunCommand(context.Background(), script, timeout)
	if len(output) > uploadBytesLimit {
		output = "...(truncated)..." + output[len(output)-uploadBytesLimit:]
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	return shellResponse(name, output, exitCode, errStr)
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
func shellResponse(name, output string, exitCode int, errStr string) genai.FunctionResponse {
	return genai.FunctionResponse{
		Name:     name,
		Response: map[string]any{"output": output, "exit_code": exitCode, "error": errStr},
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
			"output":    {Type: genai.TypeString, Description: "The merged stdout/stderr of the command"},
			"exit_code": {Type: genai.TypeInteger, Description: "The command's exit code"},
			"error":     {Type: genai.TypeString, Description: "The error running the command, if it could not be executed"},
		},
	}
}

// dockerExecInContainerScript wraps command to run inside the retained build
// container. prepareBuild leaves exactly one rb-* container between builds
// which is used here by starting (if stopped) and exec'ing the command there
// so the in-container exit code propagates. Exits 125 when no build has run.
func dockerExecInContainerScript(command string) string {
	return `c=$(docker ps -aq --filter name=^rb- | head -1); ` +
		`[ -n "$c" ] || { echo 'no build container found; run a build first' >&2; exit 125; }; ` +
		`docker start "$c" >/dev/null; ` +
		`exec docker exec "$c" /bin/sh -c ` + posixSingleQuote(command)
}

// posixSingleQuote renders s as a single-quoted POSIX sh word, escaping any
// embedded single quotes.
func posixSingleQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

func (a *defaultAgent) proposeInferenceWithAIAssist(ctx context.Context, initialErr error, wt billy.Filesystem, str *memory.Storage) (*schema.StrategyOneOf, error) {
	prompt := []string{
		fmt.Sprintf("Based on the following inference failure error \"%v\" for package '%s', find the correct source code repository URL.", initialErr, a.t.Package),
		"Just return the URL WITHOUT any additional text or formatting.",
		"For example, for the package 'org.apache.camel:camel-support', return 'https://github.com/apache/camel' not 'https://github.com/apache/camel/tree/main/core/camel-support'.",
		"Use the tools you have at your disposal to find the URL.",
		"Finally, if you don't find the URL, just return an empty string.",
	}
	repoURL, usage, err := llm.GenerateTextContentWithUsage(ctx, a.deps.GenaiClient, cmp.Or(a.deps.Model, llm.GeminiPro), &genai.GenerateContentConfig{
		Temperature: genai.Ptr(float32(0.0)),
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
	}, genai.NewPartFromText(strings.Join(prompt, "\n")))
	a.addSideUsage(usage)
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
			HTTPClient: http.DefaultClient,
			GitCache:   a.deps.GitCache,
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
			GitCache:   a.deps.GitCache,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{
					Worktree: wt,
					Storer:   str,
				}
			},
		},
	)
	if err != nil {
		wt = memfs.New()
		str = memory.NewStorage()
		s, err = a.proposeInferenceWithAIAssist(ctx, err, wt, str)
		if err != nil {
			return nil, errors.Wrap(err, "AI-assisted inference failed")
		}
		log.Println("AI-assisted inference succeeded.")
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
	}
}

func (a *defaultAgent) diagnoseOnly() []string {
	prompt := []string{
		"Please explain what went wrong with the rebuild, and what might need to be changed to resolve the build.",
		"You can include in-line code snippets, but the overall description should only be two or three sentences.",
		"To debug the build, you might want to use the read_build_logs tool to view the build errors to understand what's going wrong.",
		"You might also want to inspect the contents of the source repo using the read_repo_file or list_repo_files tools.",
	}
	if a.deps.ScratchRunner != nil {
		prompt = append(prompt,
			"You can also use the run_on_host tool to run diagnostic shell commands on the build VM host (e.g. inspect docker images or examine files left by previous build attempts), and the run_in_container tool to run commands inside the retained build container with the build's own toolchain.",
		)
	}
	return append(prompt,
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
		"You SHOULD NOT change the ref in the location.",
		"To debug the build, you might want to use the read_build_logs tool to view the build errors to understand what's going wrong.",
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
		inst, err := s.GenerateFor(a.t, rebuild.BuildEnv{
			TimewarpHost: "localhost:8080",
			HasRepo:      false,
		})
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
	return response.Parts[0].Text, nil
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
	strat := rebuild.ManualStrategy{
		Location:   a.loc,
		Requires:   prev.Instructions.Requires,
		Deps:       prev.Instructions.Deps,
		Build:      thought.UpdatedScript,
		OutputPath: prev.Instructions.OutputPath,
	}
	stratOneOf := schema.NewStrategyOneOf(&strat)
	return &stratOneOf, nil
}

func (a *defaultAgent) Propose(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	// For the first iteration, use our regular inference logic.
	// This allows the agent to benefit from the rest of our infrence improvements.
	if len(a.iterHistory) == 0 {
		return a.proposeNormalInference(ctx)
	} else {
		return a.proposeAgentInference(ctx, opts)
	}
}

func (a *defaultAgent) RecordIteration(i *schema.AgentIteration) {
	a.iterHistory = append(a.iterHistory, i)
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
