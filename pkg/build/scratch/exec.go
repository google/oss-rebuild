// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scratch implements build.Executor over the scratch VM exec API.
//
// Builds execute on a per-session scratch VM through the agent API's
// broker-proxied exec operations (/scratch/exec/op/create|get). The VM has no
// storage access of its own: command output is read from the GCS object the
// broker syncs (ScratchExecResult.OutURI), and artifacts are retrieved by
// base64-encoding them over the exec output channel.
package scratch

import (
	"context"
	"io"
	"net/url"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

const (
	// defaultPollInterval is the steady-state gap between exec op polls.
	defaultPollInterval = 10 * time.Second
	// shortPollInterval is used before the first status observation so
	// quick commands round-trip fast.
	shortPollInterval = time.Second
	// pollBackstopSlack extends the client-side polling deadline beyond the
	// op's worker-enforced timeout. The worker and broker are authoritative
	// for termination. This only guards against a wedged control plane.
	pollBackstopSlack = 10 * time.Minute
	// maxConsecutivePollErrs bounds tolerated transient poll failures.
	maxConsecutivePollErrs = 5
)

// Stubs bundles the agent-api scratch exec endpoints.
type Stubs struct {
	ExecCreate api.StubFn[schema.ScratchExecRequest, longrunning.Operation[schema.ScratchExecResult]]
	ExecGet    api.StubFn[schema.GetOperationRequest, longrunning.Operation[schema.ScratchExecResult]]
}

// Exec dispatches one exec op and polls it to a terminal state. The returned
// operation is always terminal (with a non-nil Result) when err is nil. The
// op may still describe a failure via Operation.Error or a nonzero exit code.
// A zero pollInterval uses the package default.
//
// Cancelling ctx stops polling only: with no worker kill endpoint, the remote
// command runs until its worker-enforced TimeoutSeconds.
func Exec(ctx context.Context, stubs Stubs, req schema.ScratchExecRequest, pollInterval time.Duration) (*longrunning.Operation[schema.ScratchExecResult], error) {
	return exec(ctx, stubs, req, pollInterval, nil)
}

// exec is Exec with an optional per-poll observer, used by the executor to
// follow output while the op is pending. observe is also called once after
// the terminal observation.
func exec(ctx context.Context, stubs Stubs, req schema.ScratchExecRequest, pollInterval time.Duration, observe func(*longrunning.Operation[schema.ScratchExecResult])) (*longrunning.Operation[schema.ScratchExecResult], error) {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	op, err := stubs.ExecCreate(ctx, req)
	if err != nil {
		return nil, errors.Wrap(err, "creating exec")
	}
	// The worker enforces TimeoutSeconds and the broker finalizes lost
	// execs. This deadline is a generous backstop against a wedged control
	// plane. TimeoutSeconds of 0 means broker-default, so backstop
	// generously.
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 2 * time.Hour
	}
	deadline := time.Now().Add(timeout + pollBackstopSlack)
	// Poll quickly until the first successful observation so trivial
	// commands round-trip in about a second, then back off.
	interval := min(shortPollInterval, pollInterval)
	var pollErrs int
	for !op.Done {
		if time.Now().After(deadline) {
			return nil, errors.Errorf("exec %s not terminal after %s", op.ID, timeout+pollBackstopSlack)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		next, err := stubs.ExecGet(ctx, schema.GetOperationRequest{ID: op.ID})
		if err != nil {
			// Tolerate transient poll failures. The op remains authoritative
			// server-side.
			pollErrs++
			if pollErrs >= maxConsecutivePollErrs {
				// NOTE: Giving up strands the remote command: with no kill
				// endpoint it keeps running (and occupying the single-build
				// VM) until its worker-enforced timeout, so a brief broker
				// outage can leave an abandoned build contending with the
				// next one dispatched.
				return nil, errors.Wrap(err, "polling exec")
			}
			continue
		}
		pollErrs = 0
		op = next
		interval = pollInterval
		if observe != nil && !op.Done {
			observe(op)
		}
	}
	if op.Result == nil {
		return nil, errors.Errorf("exec %s returned no result", op.ID)
	}
	if observe != nil {
		observe(op)
	}
	return op, nil
}

// outputObject resolves the op's OutURI to a GCS object handle, or nil when
// the op has no output object (no output was ever synced).
func outputObject(client *gcs.Client, op *longrunning.Operation[schema.ScratchExecResult]) (*gcs.ObjectHandle, error) {
	if op.Result == nil || op.Result.OutURI == "" {
		return nil, nil
	}
	u, err := url.Parse(op.Result.OutURI)
	if err != nil {
		return nil, errors.Wrapf(err, "parsing out URI %q", op.Result.OutURI)
	}
	if u.Scheme != "gs" || u.Host == "" {
		return nil, errors.Errorf("unexpected out URI %q", op.Result.OutURI)
	}
	return client.Bucket(u.Host).Object(strings.TrimPrefix(u.Path, "/")), nil
}

// ReadOutput reads the exec op's merged stdout/stderr object. A positive
// limit reads at most the last limit bytes. Zero reads the entire object. A
// missing object (no output produced) yields nil bytes.
func ReadOutput(ctx context.Context, client *gcs.Client, op *longrunning.Operation[schema.ScratchExecResult], limit int64) ([]byte, error) {
	obj, err := outputObject(client, op)
	if err != nil || obj == nil {
		return nil, err
	}
	var rd *gcs.Reader
	if limit > 0 {
		rd, err = obj.NewRangeReader(ctx, -limit, -1)
	} else {
		rd, err = obj.NewReader(ctx)
	}
	if errors.Is(err, gcs.ErrObjectNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "opening output object")
	}
	defer rd.Close()
	b, err := io.ReadAll(rd)
	if err != nil {
		return nil, errors.Wrap(err, "reading output object")
	}
	return b, nil
}
