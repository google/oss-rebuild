// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"fmt"
	"html/template"
	"io"
	"log"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

const (
	// defaultCommandTimeout bounds LLM-initiated diagnostic commands.
	defaultCommandTimeout = 5 * time.Minute
	// keepAliveInterval spaces the no-op execs that mark the scratch VM
	// active while the session waits on work that produces no exec traffic.
	// NOTE: Must stay comfortably inside the idle reaper's threshold (30m).
	keepAliveInterval = 5 * time.Minute
	// diffTimeout bounds the on-VM artifact comparison: fetching the tools
	// and the upstream artifact, then stabilizing and diffing.
	diffTimeout = 10 * time.Minute
	// exitDiffSetupFailed is the comparison script's exit for any failure
	// before diffr runs, with the reason on stdout. It is disjoint from
	// diffr's own codes (0 same, 1 differ, 127 error).
	exitDiffSetupFailed = 3
)

// ScratchRunner executes iteration builds on the session's scratch VM via a
// build.Executor and evaluates each produced artifact against upstream.
// NOTE: The runner does not manage the scratch lifecycle. The VM is allocated
// by the session creator and torn down at session completion (or by the idle
// reaper).
// TODO: Prune old per-build directories via the raw exec stubs: the executor
// keeps every <workdir>/builds entry for post-mortem inspection, so a long
// session can fill the VM's disk.
type ScratchRunner struct {
	Target rebuild.Target
	// Executor runs iteration builds (a scratch.DockerRunExecutor in production).
	Executor build.Executor
	// ScratchID, Stubs, and GCSClient power the run_on_host and
	// run_in_container LLM tools' direct exec access to the VM.
	ScratchID string
	Stubs     scratch.Stubs
	GCSClient *gcs.Client
	// RegistryClient makes upstream registry requests during verification.
	RegistryClient httpx.BasicClient
	// PrebuildConfig locates prebuilt tools (timewarp, diffr, stabilize)
	// fetched by build scripts and the on-VM artifact comparison.
	PrebuildConfig rebuild.PrebuildConfig
	// AuthHeader supplies the Authorization header for fetching prebuilt
	// tools onto the VM. Nil when the prebuild bucket needs none.
	AuthHeader func(context.Context) (string, error)
	// WorkDir is the VM directory builds are staged under. Defaults to
	// scratch.DefaultWorkDir.
	WorkDir string
	// BuildTimeout bounds one iteration's build. Zero uses the executor default.
	BuildTimeout time.Duration

	mu sync.Mutex
	// lastAssets is the previous iteration's asset store (RebuildAsset,
	// DebugLogsAsset), backing the read_logs_end tool.
	lastAssets rebuild.LocatableAssetStore
	lastRunID  string // the build the VM retains
}

// LastRunID is the most recent build's ID, or "" before any.
func (r *ScratchRunner) LastRunID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRunID
}

// LastBuildLogs returns the merged output of the most recent iteration
// build, or an error if no build has run or produced logs.
func (r *ScratchRunner) LastBuildLogs(ctx context.Context) ([]byte, error) {
	r.mu.Lock()
	store := r.lastAssets
	r.mu.Unlock()
	if store == nil {
		return nil, errors.New("no previous build execution")
	}
	rd, err := store.Reader(ctx, rebuild.DebugLogsAsset.For(r.Target))
	if err != nil {
		return nil, errors.Wrap(err, "reading build logs")
	}
	defer rd.Close()
	return io.ReadAll(rd)
}

func (r *ScratchRunner) setLast(runID string, store rebuild.LocatableAssetStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRunID, r.lastAssets = runID, store
}

// Run executes the proposed strategy on the scratch VM and returns the
// iteration status (one of the schema.AgentIterationStatus* values) along
// with the build result to record. Failures are folded into the returned
// status/result rather than surfaced as errors so every attempt is
// recordable.
func (r *ScratchRunner) Run(ctx context.Context, runID string, oneof *schema.StrategyOneOf) (string, *schema.AgentBuildResult) {
	status, result := r.run(ctx, runID, oneof)
	if !result.BuildSuccess {
		log.Printf("scratch run %s: %s: %s", runID, status, result.ErrorMessage)
	}
	return status, result
}

func errorResult(err error) (string, *schema.AgentBuildResult) {
	return schema.AgentIterationStatusError, &schema.AgentBuildResult{BuildSuccess: false, ErrorMessage: err.Error()}
}

func failedResult(msg string) (string, *schema.AgentBuildResult) {
	return schema.AgentIterationStatusFailed, &schema.AgentBuildResult{BuildSuccess: false, ErrorMessage: msg}
}

func (r *ScratchRunner) run(ctx context.Context, runID string, oneof *schema.StrategyOneOf) (string, *schema.AgentBuildResult) {
	strategy, err := oneof.Strategy()
	if err != nil {
		return errorResult(errors.Wrap(err, "extracting strategy"))
	}
	input := rebuild.Input{Target: r.Target, Strategy: strategy}
	rebuilder, ok := meta.AllRebuilders[r.Target.Ecosystem]
	if !ok {
		return errorResult(errors.Errorf("unsupported ecosystem %q", r.Target.Ecosystem))
	}
	useTimewarp := rebuilder.UsesTimewarp(input)
	if useTimewarp && r.PrebuildConfig.Bucket == "" {
		return errorResult(errors.New("build requires timewarp but no prebuild bucket is configured"))
	}
	toolURLs := map[build.ToolType]string{}
	if r.PrebuildConfig.Bucket != "" {
		toolURLs[build.TimewarpTool] = "gs://" + path.Join(r.PrebuildConfig.Bucket, r.PrebuildConfig.Dir, "timewarp")
	}
	var authRequired []string
	if r.PrebuildConfig.Auth {
		authRequired = append(authRequired, "gs://"+r.PrebuildConfig.Bucket)
	}
	store := rebuild.NewFilesystemAssetStore(memfs.New())
	h, err := r.Executor.Start(ctx, input, build.Options{
		BuildID:     runID,
		UseTimewarp: useTimewarp,
		Timeout:     r.BuildTimeout,
		Resources: build.Resources{
			AssetStore:       store,
			ToolURLs:         toolURLs,
			ToolAuthRequired: authRequired,
			BaseImageConfig:  build.DefaultBaseImageConfig(),
		},
	})
	if err != nil {
		return errorResult(errors.Wrap(err, "starting build"))
	}
	result, err := h.Wait(ctx)
	// Retain whatever assets the build produced (debug logs upload even on
	// failure) for the read_logs_end tool.
	r.setLast(runID, store)
	if err != nil {
		return errorResult(errors.Wrap(err, "awaiting build"))
	}
	if result.Error != nil {
		// Build failures (nonzero exit, worker-enforced timeout, output cap,
		// missing artifact) are FAILED: attempts the LLM can iterate on.
		// Anything else is infrastructure and recorded as ERROR.
		// NOTE: ERROR attempts are neither recorded nor shown to the LLM, so
		// a deterministic error (e.g. an artifact too large to retrieve)
		// recurs identically each iteration until the budget is exhausted.
		switch {
		case errors.As(result.Error, new(*scratch.ExitError)),
			errors.Is(result.Error, context.DeadlineExceeded),
			errors.Is(result.Error, scratch.ErrOutputCapped),
			errors.Is(result.Error, scratch.ErrNoArtifact):
			return failedResult(result.Error.Error())
		default:
			return errorResult(result.Error)
		}
	}
	exactMatch, stabilizedMatch, diffSummary, err := r.verify(ctx, runID, store)
	if err != nil {
		return errorResult(errors.Wrap(err, "verifying artifact"))
	}
	if !exactMatch && !stabilizedMatch {
		return failedResult(r.mismatchMessage(runID, diffSummary))
	}
	return schema.AgentIterationStatusSuccess, &schema.AgentBuildResult{BuildSuccess: true}
}

// mismatchMessage is the content-mismatch error shown to the LLM: the
// file-level summary when one was produced, how to see content-level
// differences on the VM, and where the container's source tree is on the
// host so anything built there can be compared the same way.
func (r *ScratchRunner) mismatchMessage(runID, diffSummary string) string {
	msg := "rebuild content mismatch"
	if diffSummary == "" {
		return msg
	}
	p := r.vmPaths(runID)
	return msg + "\n\n" + verifier.DiffSummaryPreamble + "\n" + diffSummary +
		"\n" + verifier.MismatchGuidance +
		fmt.Sprintf("\nBoth artifacts remain on the VM, stabilized beside the originals. For content-level differences run diffr on the host with run_on_host:\n%s %s.stabilized %s.stabilized\n(--json for machine-readable output). The retained container's /src is the host directory %s, so an artifact you build in the container can be compared the same way at that path once stabilized:\n%s --ecosystem %s --artifact %s --infile <file> --outfile <file>.stabilized\nHost tools cannot read other container paths.",
			p.diffr, p.rebuild, p.upstream, p.src, p.stabilize, r.Target.Ecosystem, r.Target.Artifact)
}

// verify compares the rebuilt artifact (in store) against upstream,
// returning whether the bytes match exactly and after stabilization. On
// mismatch it also returns the file-level summary produced on the VM for the
// LLM. A summary failure is logged rather than returned, so the mismatch is
// still reported.
func (r *ScratchRunner) verify(ctx context.Context, runID string, store rebuild.LocatableAssetStore) (exactMatch, stabilizedMatch bool, diffSummary string, err error) {
	stabilizers, err := stability.StabilizersForTarget(r.Target)
	if err != nil {
		return false, false, "", errors.Wrap(err, "getting stabilizers")
	}
	upstreamURI, err := r.upstreamURL(ctx)
	if err != nil {
		return false, false, "", errors.Wrap(err, "getting upstream url")
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, r.RegistryClient)
	rb, up, err := verifier.SummarizeArtifacts(ctx, store, r.Target, upstreamURI, []crypto.Hash{crypto.SHA256}, stabilizers)
	if err != nil {
		return false, false, "", errors.Wrap(err, "summarizing artifacts")
	}
	exactMatch = bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	stabilizedMatch = bytes.Equal(rb.StabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil))
	if !exactMatch && !stabilizedMatch {
		summary, derr := r.diffOnVM(ctx, runID, upstreamURI)
		if derr != nil {
			log.Printf("scratch run %s: summarizing artifact diff: %v", runID, derr)
		}
		diffSummary = summary
	}
	return exactMatch, stabilizedMatch, diffSummary, nil
}

// upstreamURL is where the target's published artifact is fetched from.
func (r *ScratchRunner) upstreamURL(ctx context.Context) (string, error) {
	rebuilder, ok := meta.AllRebuilders[r.Target.Ecosystem]
	if !ok {
		return "", errors.Errorf("unsupported ecosystem %q", r.Target.Ecosystem)
	}
	return rebuilder.UpstreamURL(ctx, r.Target, meta.NewRegistryMux(r.RegistryClient))
}

// vmPaths are the comparison inputs on the VM: the build's artifact where the
// executor left it, and the upstream artifact and the tools under the work
// directory, fetched once per session. Each artifact's stabilized form is
// written beside it, except a checked candidate's, which goes in checks to
// keep the source tree clean.
type vmPaths struct {
	rebuild, src, upstream, diffr, stabilize, checks string
}

func (r *ScratchRunner) vmPaths(runID string) vmPaths {
	workDir := cmp.Or(r.WorkDir, scratch.DefaultWorkDir)
	return vmPaths{
		rebuild:   scratch.ArtifactPath(workDir, runID),
		src:       scratch.SourcePath(workDir, runID),
		upstream:  path.Join(workDir, "upstream", path.Base(r.Target.Artifact)),
		diffr:     path.Join(workDir, "tools", string(build.DiffrTool)),
		stabilize: path.Join(workDir, "tools", string(build.StabilizeTool)),
		checks:    path.Join(workDir, "checks"),
	}
}

// toolURL is the fetchable URL of a prebuilt tool in the prebuild bucket.
func (r *ScratchRunner) toolURL(tool build.ToolType) (string, error) {
	u, err := build.ConvertURLForRuntime("gs://" + path.Join(r.PrebuildConfig.Bucket, r.PrebuildConfig.Dir, string(tool)))
	return u, errors.Wrapf(err, "resolving the %s URL", tool)
}

// scriptPrelude defines the helpers the comparison scripts share.
const scriptPrelude = `set -eu
die() { echo "$*"; exit {{.ExitSetupFailed}}; }
stab() { out=$({{.Stabilize}} --ecosystem {{.Ecosystem}} --artifact {{.Artifact}} --infile "$1" --outfile "$2.part" 2>&1) && mv "$2.part" "$2" || die "stabilizing $1 failed: $out"; }
`

// compareScriptTpl stages the tools and upstream once, then diffs the
// stabilized forms.
var compareScriptTpl = template.Must(template.New("compare").Parse(scriptPrelude + `fetch() { [ -e "$1" ] || { wget -q ${3:+--header "$3"} -O "$1.part" "$2" && mv "$1.part" "$1"; } || die "fetching $2 failed"; }
mkdir -p {{.Tools}} {{.UpstreamDir}}
fetch {{.Diffr}} {{.DiffrURL}} "${AUTH_HEADER:-}"
fetch {{.Stabilize}} {{.StabilizeURL}} "${AUTH_HEADER:-}"
fetch {{.Upstream}} {{.UpstreamURL}}
chmod +x {{.Diffr}} {{.Stabilize}}
[ -e {{.Upstream}}.stabilized ] || stab {{.Upstream}} {{.Upstream}}.stabilized
{{if .Rebuild}}stab {{.Rebuild}} {{.Rebuild}}.stabilized
exec {{.Diffr}} --summary --label rebuild --label upstream {{.Rebuild}}.stabilized {{.Upstream}}.stabilized
{{end}}`))

// shellWord matches what the comparison script's inputs may contain: the
// path, URL and package-name characters, and none that the shell interprets.
var shellWord = regexp.MustCompile(`^[A-Za-z0-9./_@+:%~-]+$`)

// renderScript executes tpl with args plus the tool, upstream, and target
// inputs every comparison script takes. The inputs are spliced unquoted, so
// each must be a shellWord.
func (r *ScratchRunner) renderScript(tpl *template.Template, p vmPaths, args map[string]any) (string, error) {
	args["Diffr"], args["Stabilize"], args["Upstream"] = p.diffr, p.stabilize, p.upstream
	args["Ecosystem"], args["Artifact"] = string(r.Target.Ecosystem), r.Target.Artifact
	for k, v := range args {
		if !shellWord.MatchString(v.(string)) {
			return "", errors.Errorf("%s %q is not a shell word", k, v)
		}
	}
	args["ExitSetupFailed"] = exitDiffSetupFailed
	var b strings.Builder
	err := tpl.Execute(&b, args)
	return b.String(), err
}

// compareScript renders the script to stage, stabilize, and diff on the VM.
// An empty rebuild only stages.
func (r *ScratchRunner) compareScript(p vmPaths, upstreamURI, rebuild string) (string, error) {
	diffrURL, err := r.toolURL(build.DiffrTool)
	if err != nil {
		return "", err
	}
	stabilizeURL, err := r.toolURL(build.StabilizeTool)
	if err != nil {
		return "", err
	}
	args := map[string]any{
		"Tools":        path.Dir(p.diffr),
		"DiffrURL":     diffrURL,
		"StabilizeURL": stabilizeURL,
		"UpstreamDir":  path.Dir(p.upstream),
		"UpstreamURL":  upstreamURI,
	}
	if rebuild != "" {
		args["Rebuild"] = rebuild
	}
	return r.renderScript(compareScriptTpl, p, args)
}

// compareExec runs script on the VM, with the prebuild credential if auth.
func (r *ScratchRunner) compareExec(ctx context.Context, script string, auth bool) (exitCode int, out []byte, err error) {
	req := schema.ScratchExecRequest{
		ScratchID:      r.ScratchID,
		Cmd:            []string{"/bin/sh", "-c", script},
		TimeoutSeconds: int(diffTimeout.Seconds()),
	}
	if auth && r.AuthHeader != nil {
		header, err := r.AuthHeader(ctx)
		if err != nil {
			return 0, nil, errors.Wrap(err, "minting prebuild auth")
		}
		req.Env = map[string]string{"AUTH_HEADER": header}
	}
	op, err := scratch.Exec(ctx, r.Stubs, req, 0)
	if err != nil {
		return 0, nil, errors.Wrap(err, "running the comparison")
	}
	if op.Error != nil {
		return 0, nil, errors.Wrap(op.Error, "comparison exec")
	}
	out, err = scratch.ReadOutput(ctx, r.GCSClient, op, 0)
	return op.Result.ExitCode, out, errors.Wrap(err, "reading the comparison output")
}

// diffOnVM runs compareScript on the VM and returns diffr's file-level summary
// of the stabilized artifacts, capped like tool output.
func (r *ScratchRunner) diffOnVM(ctx context.Context, runID, upstreamURI string) (string, error) {
	p := r.vmPaths(runID)
	script, err := r.compareScript(p, upstreamURI, p.rebuild)
	if err != nil {
		return "", errors.Wrap(err, "rendering diff script")
	}
	code, out, rerr := r.compareExec(ctx, script, true)
	switch code {
	case 1:
		if rerr != nil {
			return "", rerr
		}
		return headLimit(string(out), uploadBytesLimit), nil
	case 0:
		if rerr != nil {
			return "", rerr
		}
		return "(diffr reports no file-level differences after stabilization)\n", nil
	case exitDiffSetupFailed:
		return "", errors.Errorf("preparing the comparison on the VM: %s", strings.TrimSpace(string(out)))
	default:
		if rerr != nil {
			return "", rerr
		}
		return "", errors.Errorf("diffr exited %d: %s", code, strings.TrimSpace(string(out)))
	}
}

// PrepareCompare stages the tools and the stabilized upstream on the VM before
// any build, so a candidate can be checked at once. Idempotent.
func (r *ScratchRunner) PrepareCompare(ctx context.Context) error {
	upstreamURI, err := r.upstreamURL(ctx)
	if err != nil {
		return errors.Wrap(err, "getting upstream url")
	}
	script, err := r.compareScript(r.vmPaths(""), upstreamURI, "")
	if err != nil {
		return errors.Wrap(err, "rendering the prepare script")
	}
	code, out, err := r.compareExec(ctx, script, true)
	if err != nil {
		return err
	}
	if code != 0 {
		return errors.Errorf("preparing the comparison on the VM: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// headLimit keeps the first limit bytes of s, marking any cut.
func headLimit(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... (truncated)\n"
}

// StartKeepAlive periodically issues a no-op exec against the scratch VM so
// the idle reaper doesn't tear it down while the session is busy with work
// that generates no exec traffic (notably the GCB confirmation build, which
// can exceed the idle threshold). The returned stop function ends the
// keepalive.
func (r *ScratchRunner) StartKeepAlive(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(keepAliveInterval):
			}
			if _, err := scratch.Exec(ctx, r.Stubs, schema.ScratchExecRequest{
				ScratchID:      r.ScratchID,
				Cmd:            []string{"/bin/true"},
				TimeoutSeconds: 60,
			}, 0); err != nil && ctx.Err() == nil {
				log.Printf("scratch keepalive: %v", err)
			}
		}
	}()
	return cancel
}

// RunCommand executes a diagnostic shell command on the scratch VM on
// behalf of the LLM and returns its exit code and merged output (truncated
// to the tail when large). The returned error covers infrastructure
// failures only. Command failures are expressed via the exit code.
func (r *ScratchRunner) RunCommand(ctx context.Context, command string, timeoutSeconds int) (exitCode int, output string, err error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(defaultCommandTimeout.Seconds())
	}
	op, err := scratch.Exec(ctx, r.Stubs, schema.ScratchExecRequest{
		ScratchID:      r.ScratchID,
		Cmd:            []string{"/bin/sh", "-c", command},
		TimeoutSeconds: timeoutSeconds,
	}, 0)
	if err != nil {
		return 0, "", err
	}
	out, rerr := scratch.ReadOutput(ctx, r.GCSClient, op, uploadBytesLimit)
	if rerr != nil {
		log.Printf("scratch command: reading output: %v", rerr)
	}
	if op.Error != nil {
		if op.Error.Code == int(codes.DeadlineExceeded) {
			return op.Result.ExitCode, string(out), commandTimeout(timeoutSeconds)
		}
		return 0, string(out), errors.New(op.Error.Message)
	}
	return op.Result.ExitCode, string(out), nil
}

// checkScriptTpl stabilizes a candidate into the checks directory, replacing
// any earlier check of the same file name, and prints both sha256 with their
// paths. On a mismatch, diffr's summary and exit follow.
var checkScriptTpl = template.Must(template.New("check").Parse(scriptPrelude + `[ -f {{.Candidate}} ] || die "{{.Candidate}}: no such file on the VM host"
for f in {{.Upstream}} {{.Stabilize}} {{.Diffr}}; do [ -e $f ] || die "$f is not on the VM; the job stages the upstream artifact and the tools before each session"; done
[ -e {{.Upstream}}.stabilized ] || stab {{.Upstream}} {{.Upstream}}.stabilized
mkdir -p {{.Checks}}
stab {{.Candidate}} {{.Stabilized}}
c=$(sha256sum {{.Stabilized}} | cut -c1-64)
u=$(sha256sum {{.Upstream}}.stabilized | cut -c1-64)
echo "$c $u {{.Stabilized}} {{.Upstream}}.stabilized"
[ "$c" != "$u" ] || exit 0
exec {{.Diffr}} --summary --label candidate --label upstream {{.Stabilized}} {{.Upstream}}.stabilized
`))

// checkScript renders checkScriptTpl for candidate, a path on the VM host.
func (r *ScratchRunner) checkScript(p vmPaths, candidate string) (string, error) {
	return r.renderScript(checkScriptTpl, p, map[string]any{
		"Candidate":  candidate,
		"Checks":     p.checks,
		"Stabilized": path.Join(p.checks, path.Base(candidate)+".stabilized"),
	})
}

// CheckArtifact compares candidate, a file on the VM host, with the staged
// upstream artifact. exitCode is 0 on a match, 1 on a mismatch with diffr's
// summary in out, and exitDiffSetupFailed when an input is missing or fails
// to stabilize.
//
// NOTE: A mismatch is not an error. err means the check could not run.
func (r *ScratchRunner) CheckArtifact(ctx context.Context, candidate string) (exitCode int, out []byte, err error) {
	script, err := r.checkScript(r.vmPaths(""), candidate)
	if err != nil {
		return 0, nil, errors.Wrap(err, "rendering the check script")
	}
	return r.compareExec(ctx, script, false)
}
