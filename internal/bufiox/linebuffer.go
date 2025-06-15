// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package bufiox

import (
	"bytes"
	"errors"
	"sync"
)

// LineBuffer is a thread-safe ring buffer for lines of text.
// Its capacity is defined in bytes, and it operates on a single underlying
// byte slice to minimize memory allocations. It implements io.ReadWriter.
type LineBuffer struct {
	buf           []byte // The circular byte buffer.
	capacity      int    // The total size of buf.
	mu            sync.Mutex
	size          int   // The number of bytes currently in use.
	head          int   // The read pointer (start of the oldest line).
	tail          int   // The write pointer (next available write position).
	lineLengths   []int // A queue of line lengths, corresponding to data in buf.
	pendingLength int   // The length of the unfinished line at the end of the buf.
}

// NewLineBuffer creates a new LineBuffer with the specified capacity in bytes.
func NewLineBuffer(capacity int) *LineBuffer {
	if capacity <= 0 {
		panic("capacity must be positive")
	}
	return &LineBuffer{
		buf:         make([]byte, capacity),
		capacity:    capacity,
		lineLengths: make([]int, 0),
	}
}

// Write implements io.Writer. It writes data to the buffer, splitting it into lines.
// When the buffer is full, it evicts complete lines from the beginning to make space.
func (lb *LineBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	totalWritten := 0
	data := p
	for len(data) > 0 {
		// Find the next newline
		newlineIdx := bytes.IndexByte(data, '\n')
		var toWrite []byte
		if newlineIdx >= 0 {
			// Include the newline in the line
			toWrite = data[:newlineIdx+1]
			data = data[newlineIdx+1:]
		} else {
			// No newline found, write the rest as a pending line
			toWrite = data
			data = nil
		}
		// Check if we need to make space
		bytesNeeded := len(toWrite)
		for lb.size+bytesNeeded > lb.capacity {
			if !lb.evictOldestLine() {
				// Can't evict any more lines (buffer too small for current data)
				if totalWritten == 0 {
					return 0, errors.New("buffer too small for data")
				}
				return totalWritten, nil
			}
		}
		// Write the data to the buffer
		written := lb.writeToBuffer(toWrite)
		totalWritten += written
		// Update line tracking
		if newlineIdx >= 0 {
			// Complete line written
			if lb.pendingLength > 0 {
				// Combine with pending data
				lb.lineLengths = append(lb.lineLengths, lb.pendingLength+written)
				lb.pendingLength = 0
			} else {
				// New complete line
				lb.lineLengths = append(lb.lineLengths, written)
			}
		} else {
			// Partial line
			lb.pendingLength += written
		}
	}
	return totalWritten, nil
}

// Read implements io.Reader. It reads available data from the buffer.
func (lb *LineBuffer) Read(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if lb.size == 0 {
		// NOTE: Empty buffer does not return an error.
		return 0, nil
	}
	n = min(len(p), lb.size)
	// Read from the circular buffer
	if lb.head+n <= lb.capacity {
		// Simple case: no wrap around
		copy(p, lb.buf[lb.head:lb.head+n])
	} else {
		// Wrap around case
		firstPart := lb.capacity - lb.head
		copy(p[:firstPart], lb.buf[lb.head:])
		copy(p[firstPart:n], lb.buf[:n-firstPart])
	}
	// Update read pointer
	lb.head = (lb.head + n) % lb.capacity
	lb.size -= n
	lb.updateLineLengthsAfterRead(n)
	return n, nil
}

// writeToBuffer writes data to the circular buffer starting at tail.
func (lb *LineBuffer) writeToBuffer(data []byte) int {
	n := len(data)
	if n == 0 {
		return 0
	}
	if lb.tail+n <= lb.capacity {
		// Simple case: no wrap around
		copy(lb.buf[lb.tail:], data)
	} else {
		// Wrap around case
		firstPart := lb.capacity - lb.tail
		copy(lb.buf[lb.tail:], data[:firstPart])
		copy(lb.buf[:n-firstPart], data[firstPart:])
	}
	// Update write pointer
	lb.tail = (lb.tail + n) % lb.capacity
	lb.size += n
	return n
}

// evictOldestLine removes the oldest complete line from the buffer.
// Returns true if a line was evicted, false if no complete lines exist.
func (lb *LineBuffer) evictOldestLine() bool {
	if len(lb.lineLengths) == 0 {
		return false
	}
	// Remove the oldest line
	lineLen := lb.lineLengths[0]
	lb.lineLengths = lb.lineLengths[1:]
	// Update read pointer
	lb.head = (lb.head + lineLen) % lb.capacity
	lb.size -= lineLen
	return true
}

// updateLineLengthsAfterRead updates line lengths after a read operation.
func (lb *LineBuffer) updateLineLengthsAfterRead(bytesRead int) {
	remaining := bytesRead
	for remaining > 0 && len(lb.lineLengths) > 0 {
		if lb.lineLengths[0] <= remaining {
			// Complete line was read
			remaining -= lb.lineLengths[0]
			lb.lineLengths = lb.lineLengths[1:]
		} else {
			// Partial line was read
			lb.lineLengths[0] -= remaining
			remaining = 0
		}
	}
	// Update pending length if necessary
	if remaining > 0 && lb.pendingLength > 0 {
		if lb.pendingLength <= remaining {
			lb.pendingLength = 0
		} else {
			lb.pendingLength -= remaining
		}
	}
}

// Len returns the current number of bytes in the buffer.
func (lb *LineBuffer) Len() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.size
}

// Capacity returns the total capacity of the buffer.
func (lb *LineBuffer) Capacity() int {
	return lb.capacity
}

// Clear empties the buffer.
func (lb *LineBuffer) Clear() {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.size = 0
	lb.head = 0
	lb.tail = 0
	lb.lineLengths = lb.lineLengths[:0]
	lb.pendingLength = 0
}
