// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// Exec polls until its ctx is done, so the tool must get the session's
// context, not a fresh Background one.
func TestRunShellUnwindsOnCancellation(t *testing.T) {
	pending := func(ctx context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
		return &longrunning.Operation[schema.ScratchExecResult]{ID: "op", Done: false}, nil
	}
	polled := func(ctx context.Context, req schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
		return &longrunning.Operation[schema.ScratchExecResult]{ID: "op", Done: false}, nil
	}
	a := NewDefaultAgent(
		rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"},
		&AgentDeps{ScratchRunner: &ScratchRunner{Stubs: scratch.Stubs{ExecCreate: pending, ExecGet: polled}}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	done := make(chan string, 1)
	go func() {
		resp := a.runShell(ctx, "run_on_host", map[string]any{"command": "sleep 300"}, nil)
		errStr, _ := resp.Response["error"].(string)
		done <- errStr
	}()
	select {
	case errStr := <-done:
		if !strings.Contains(errStr, context.Canceled.Error()) {
			t.Errorf("error = %q, want it to name the cancellation", errStr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runShell did not return after its context was cancelled")
	}
}
