// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"io"
	"sync"

	"github.com/google/oss-rebuild/pkg/build"
)

// scratchHandle implements build.Handle for scratch VM builds.
type scratchHandle struct {
	id         string
	cancel     context.CancelFunc
	output     io.ReadWriteCloser // BufferedPipe for streaming output
	resultChan chan build.Result

	statusMu sync.RWMutex
	status   build.BuildState
}

// BuildID implements build.Handle
func (h *scratchHandle) BuildID() string {
	return h.id
}

// Wait implements build.Handle
func (h *scratchHandle) Wait(ctx context.Context) (build.Result, error) {
	defer h.output.Close()
	select {
	case result := <-h.resultChan:
		return result, nil
	case <-ctx.Done():
		return build.Result{}, ctx.Err()
	}
}

// OutputStream implements build.Handle
func (h *scratchHandle) OutputStream() io.Reader {
	return h.output
}

// Status implements build.Handle
func (h *scratchHandle) Status() build.BuildState {
	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	return h.status
}

// Cancel stops driving the build (polling and follow-up steps). There is no
// worker kill endpoint yet, so the remote command itself continues until its
// worker-enforced timeout: effectively build.CancelDetached semantics.
func (h *scratchHandle) Cancel() {
	defer h.output.Close()
	h.cancel()
	h.updateStatus(build.BuildStateCancelled)
}

// updateStatus updates the handle's status
func (h *scratchHandle) updateStatus(state build.BuildState) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	h.status = state
}

// setResult sets the final result if none has been set yet
func (h *scratchHandle) setResult(result build.Result) {
	select {
	case h.resultChan <- result:
	default:
		// Channel already closed or full
	}
}

// Write writes data to the output stream (implements io.Writer)
func (h *scratchHandle) Write(data []byte) (n int, err error) {
	return h.output.Write(data)
}
