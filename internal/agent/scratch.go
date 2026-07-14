// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto"
	"io"
	"log"
	"path"
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
)

// keepAliveInterval spaces the no-op execs that mark the scratch VM active
// while the session waits on work that produces no exec traffic.
// NOTE: Must stay comfortably inside the idle reaper's threshold (30m).
const keepAliveInterval = 5 * time.Minute

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
	// Executor runs iteration builds (a scratch.Executor in production).
	Executor build.Executor
	// ScratchID, Stubs, and GCSClient power the run_command LLM tool's
	// direct exec access to the VM.
	ScratchID string
	Stubs     scratch.Stubs
	GCSClient *gcs.Client
	// RegistryClient makes upstream registry requests during verification.
	RegistryClient httpx.BasicClient
	// PrebuildConfig locates prebuilt tools (timewarp) fetched by build scripts.
	PrebuildConfig rebuild.PrebuildConfig
	// BuildTimeout bounds one iteration's build. Zero uses the executor default.
	BuildTimeout time.Duration

	mu sync.Mutex
	// lastAssets is the previous iteration's asset store (RebuildAsset,
	// DebugLogsAsset), backing the read_logs_end tool.
	lastAssets rebuild.LocatableAssetStore
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

func (r *ScratchRunner) setLastAssets(store rebuild.LocatableAssetStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAssets = store
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
	r.setLastAssets(store)
	if err != nil {
		return errorResult(errors.Wrap(err, "awaiting build"))
	}
	if result.Error != nil {
		// Build failures (nonzero exit, worker-enforced timeout, missing
		// artifact) are FAILED: attempts the LLM can iterate on.
		// Anything else is infrastructure and recorded as ERROR.
		// NOTE: ERROR attempts are neither recorded nor shown to the LLM, so
		// a deterministic error (e.g. an artifact too large to retrieve)
		// recurs identically each iteration until the budget is exhausted.
		var exitErr *scratch.ExitError
		switch {
		case errors.As(result.Error, &exitErr),
			errors.Is(result.Error, context.DeadlineExceeded),
			errors.Is(result.Error, scratch.ErrNoArtifact):
			return failedResult(result.Error.Error())
		default:
			return errorResult(result.Error)
		}
	}
	exactMatch, stabilizedMatch, err := r.verify(ctx, rebuilder, store)
	if err != nil {
		return errorResult(errors.Wrap(err, "verifying artifact"))
	}
	if !exactMatch && !stabilizedMatch {
		return failedResult("rebuild content mismatch")
	}
	return schema.AgentIterationStatusSuccess, &schema.AgentBuildResult{BuildSuccess: true}
}

// verify compares the rebuilt artifact (in store) against the upstream
// artifact, returning whether the bytes match exactly and after
// stabilization.
func (r *ScratchRunner) verify(ctx context.Context, rebuilder rebuild.Rebuilder, store rebuild.LocatableAssetStore) (exactMatch, stabilizedMatch bool, err error) {
	stabilizers, err := stability.StabilizersForTarget(r.Target)
	if err != nil {
		return false, false, errors.Wrap(err, "getting stabilizers")
	}
	mux := meta.NewRegistryMux(r.RegistryClient)
	upstreamURI, err := rebuilder.UpstreamURL(ctx, r.Target, mux)
	if err != nil {
		return false, false, errors.Wrap(err, "getting upstream url")
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, r.RegistryClient)
	rb, up, err := verifier.SummarizeArtifacts(ctx, store, r.Target, upstreamURI, []crypto.Hash{crypto.SHA256}, stabilizers)
	if err != nil {
		return false, false, errors.Wrap(err, "summarizing artifacts")
	}
	exactMatch = bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	stabilizedMatch = bytes.Equal(rb.StabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil))
	return exactMatch, stabilizedMatch, nil
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
