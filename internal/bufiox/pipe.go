// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package bufiox

import (
	"io"
	"sync"
)

// BufferedPipe provides a synchronized pipe with buffering.
// It supports a single reader and single writer.
type BufferedPipe struct {
	buf      io.ReadWriter
	mu       sync.Mutex
	readCond *sync.Cond

	closed bool
}

// NewBufferedPipe creates a new BufferedPipe using the provided buffer.
func NewBufferedPipe(buf io.ReadWriter) *BufferedPipe {
	bp := &BufferedPipe{buf: buf}
	bp.readCond = sync.NewCond(&bp.mu)
	return bp
}

// Write writes data to the pipe. It never blocks.
// Returns io.ErrClosedPipe if the reader has closed.
func (bp *BufferedPipe) Write(p []byte) (n int, err error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.closed {
		return 0, io.ErrClosedPipe
	}
	n, err = bp.buf.Write(p)
	if n > 0 {
		// Wake up any waiting reader
		bp.readCond.Signal()
	}
	return n, err
}

// Read reads data from the pipe. It blocks until data is available,
// the writer closes, or the reader closes.
// Returns io.EOF when the writer has closed and the buffer is empty.
func (bp *BufferedPipe) Read(p []byte) (n int, err error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	for {
		n, err = bp.buf.Read(p)
		if err != nil {
			return n, err
		}
		if n > 0 {
			return n, nil
		}
		// Buffer is empty
		if bp.closed {
			return 0, io.EOF
		}
		// Wait for data or close
		bp.readCond.Wait()
	}
}

// Close closes the write side of the pipe.
// Any blocked Read calls will return io.EOF once the buffer is empty.
func (bp *BufferedPipe) Close() error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.closed {
		return io.ErrClosedPipe
	}
	bp.closed = true
	bp.readCond.Broadcast() // Wake all waiting readers
	return nil
}
