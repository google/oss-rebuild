// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"io"
	"sync"

	"github.com/google/oss-rebuild/pkg/build"
)

// localHandle implements build.Handle for local Docker builds
type localHandle struct {
	id         string
	cancel     context.CancelFunc
	output     io.ReadWriteCloser
	resultChan chan build.Result

	statusMu sync.RWMutex
	status   build.BuildState
}

// BuildID implements build.Handle
func (h *localHandle) BuildID() string {
	return h.id
}

// Wait implements build.Handle
func (h *localHandle) Wait(ctx context.Context) (build.Result, error) {
	defer h.output.Close()
	select {
	case result := <-h.resultChan:
		return result, nil
	case <-ctx.Done():
		// Context timeout - this is different from build cancellation
		return build.Result{}, ctx.Err()
	}
}

// OutputStream implements build.Handle
func (h *localHandle) OutputStream() io.Reader {
	return h.output
}

// Status implements build.Handle
func (h *localHandle) Status() build.BuildState {
	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	return h.status
}

// Cancel cancels the build
func (h *localHandle) Cancel() {
	defer h.output.Close()
	h.cancel()
}

// updateStatus updates the handle's status
func (h *localHandle) updateStatus(state build.BuildState) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	h.status = state
}

// setResult sets the final result and closes the result channel
func (h *localHandle) setResult(result build.Result) {
	select {
	case h.resultChan <- result:
	default:
		// Channel already closed or full
	}
}

// writeOutput writes a line to the output stream
func (h *localHandle) Write(line []byte) (n int, err error) {
	return h.output.Write(line)
}
