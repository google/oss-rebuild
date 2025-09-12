// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"io"
	"sync"

	"github.com/google/oss-rebuild/pkg/build"
	"google.golang.org/api/cloudbuild/v1"
)

// gcbHandle implements build.Handle for Google Cloud Build
type gcbHandle struct {
	id           string
	executor     *Executor
	operation    *cloudbuild.Operation
	output       io.ReadWriteCloser // BufferedPipe for streaming output
	resultChan   chan build.Result
	cancelPolicy build.CancelPolicy

	statusMu sync.RWMutex
	status   build.BuildState
}

// BuildID implements build.Handle
func (h *gcbHandle) BuildID() string {
	return h.id
}

// Wait implements build.Handle
func (h *gcbHandle) Wait(ctx context.Context) (build.Result, error) {
	defer h.output.Close()
	select {
	case result := <-h.resultChan:
		return result, nil
	case <-ctx.Done():
		h.Cancel()
		return build.Result{}, ctx.Err()
	}
}

// OutputStream implements build.Handle
func (h *gcbHandle) OutputStream() io.Reader {
	// FIXME: This never gets populated
	return h.output
}

// Status implements build.Handle
func (h *gcbHandle) Status() build.BuildState {
	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	return h.status
}

// Cancel cancels the build based on the configured policy
func (h *gcbHandle) Cancel() {
	defer h.output.Close()
	switch h.cancelPolicy {
	case build.CancelImmediate, build.CancelGraceful:
		if h.operation != nil {
			// TODO: This isn't guaranteed to be synchronous. We may want to wait on the operation.
			h.executor.client.CancelOperation(h.operation)
		}
		fallthrough
	case build.CancelDetached:
		h.updateStatus(build.BuildStateCancelled)
	}
}

// updateStatus updates the handle's status
func (h *gcbHandle) updateStatus(state build.BuildState) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()

	h.status = state
}

// setResult sets the final result and closes the result channel
func (h *gcbHandle) setResult(result build.Result) {
	select {
	case h.resultChan <- result:
	default:
		// Channel already closed or full
	}
}

// Write writes data to the output stream (implements io.Writer)
func (h *gcbHandle) Write(data []byte) (n int, err error) {
	return h.output.Write(data)
}
