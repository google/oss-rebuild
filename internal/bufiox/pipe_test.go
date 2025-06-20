// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package bufiox

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBufferedPipe(t *testing.T) {
	t.Run("BasicReadWrite", func(t *testing.T) {
		testCases := []struct {
			name     string
			capacity int
			writes   []string
			expected string
		}{
			{
				name:     "simple data",
				capacity: 100,
				writes:   []string{"Hello\n", "World\n"},
				expected: "Hello\nWorld\n",
			},
			{
				name:     "single write",
				capacity: 50,
				writes:   []string{"Single line\n"},
				expected: "Single line\n",
			},
			{
				name:     "multiple small writes",
				capacity: 200,
				writes:   []string{"A\n", "B\n", "C\n", "D\n"},
				expected: "A\nB\nC\nD\n",
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				pipe := NewBufferedPipe(lb)

				// Write in goroutine
				go func() {
					for _, write := range tc.writes {
						pipe.Write([]byte(write))
					}
					pipe.Close()
				}()

				// Read all data
				data, err := io.ReadAll(pipe)
				if err != nil {
					t.Fatalf("ReadAll failed: %v", err)
				}

				if string(data) != tc.expected {
					t.Errorf("Expected %q, got %q", tc.expected, string(data))
				}
			})
		}
	})

	t.Run("BlockingRead", func(t *testing.T) {
		testCases := []struct {
			name      string
			capacity  int
			writeData string
			readSize  int
			delay     time.Duration
		}{
			{
				name:      "short delay",
				capacity:  100,
				writeData: "test\n",
				readSize:  10,
				delay:     10 * time.Millisecond,
			},
			{
				name:      "longer delay",
				capacity:  50,
				writeData: "delayed\n",
				readSize:  20,
				delay:     50 * time.Millisecond,
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				pipe := NewBufferedPipe(lb)

				done := make(chan bool)

				// Start reader
				go func() {
					buf := make([]byte, tc.readSize)
					n, err := pipe.Read(buf)
					if err != nil || string(buf[:n]) != tc.writeData {
						t.Errorf("Read failed: n=%d, err=%v, data=%q", n, err, string(buf[:n]))
					}
					done <- true
				}()

				// Wait for reader to block
				time.Sleep(tc.delay)

				// Write data
				pipe.Write([]byte(tc.writeData))

				// Reader should unblock
				select {
				case <-done:
					// Success
				case <-time.After(100 * time.Millisecond):
					t.Error("Read did not unblock")
				}
			})
		}
	})

	t.Run("EOFAfterClose", func(t *testing.T) {
		testCases := []struct {
			name     string
			capacity int
			data     string
		}{
			{
				name:     "with data",
				capacity: 100,
				data:     "data\n",
			},
			{
				name:     "empty buffer",
				capacity: 50,
				data:     "",
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				pipe := NewBufferedPipe(lb)

				if tc.data != "" {
					pipe.Write([]byte(tc.data))
				}
				pipe.Close()

				buf := make([]byte, 10)

				if tc.data != "" {
					// First read gets data
					n, err := pipe.Read(buf)
					if err != nil || string(buf[:n]) != tc.data {
						t.Errorf("First read failed: n=%d, err=%v, data=%q", n, err, string(buf[:n]))
					}
				}

				// Next read gets EOF
				n, err := pipe.Read(buf)
				if err != io.EOF || n != 0 {
					t.Errorf("Expected EOF, got n=%d, err=%v", n, err)
				}
			})
		}
	})

	t.Run("ClosedPipeErrors", func(t *testing.T) {
		testCases := []struct {
			name      string
			closeFunc func(*BufferedPipe) error
			testWrite bool
			testRead  bool
		}{
			{
				name:      "writer closed",
				closeFunc: (*BufferedPipe).Close,
				testWrite: false,
				testRead:  false,
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(100)
				pipe := NewBufferedPipe(lb)

				tc.closeFunc(pipe)

				if tc.testWrite {
					_, err := pipe.Write([]byte("test"))
					if err != io.ErrClosedPipe {
						t.Errorf("Expected ErrClosedPipe for write, got %v", err)
					}
				}

				if tc.testRead {
					buf := make([]byte, 10)
					_, err := pipe.Read(buf)
					if err != io.ErrClosedPipe {
						t.Errorf("Expected ErrClosedPipe for read, got %v", err)
					}
				}
			})
		}
	})

	t.Run("ConcurrentOperations", func(t *testing.T) {
		testCases := []struct {
			name         string
			capacity     int
			messageCount int
			messageSize  int
		}{
			{
				name:         "small messages",
				capacity:     1000,
				messageCount: 50,
				messageSize:  10,
			},
			{
				name:         "many small messages",
				capacity:     2000,
				messageCount: 200,
				messageSize:  5,
			},
			{
				name:         "fewer large messages",
				capacity:     5000,
				messageCount: 20,
				messageSize:  50,
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				pipe := NewBufferedPipe(lb)
				var wg sync.WaitGroup
				wg.Add(2)
				// Writer
				go func() {
					defer wg.Done()
					for range tc.messageCount {
						msg := fmt.Sprintf("%s\n", strings.Repeat("A", tc.messageSize-1))
						pipe.Write([]byte(msg))
					}
					pipe.Close()
				}()
				// Reader
				received := 0
				go func() {
					defer wg.Done()
					buf := make([]byte, tc.messageSize*10)
					for {
						n, err := pipe.Read(buf)
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Errorf("Read error: %v", err)
							break
						}
						received += bytes.Count(buf[:n], []byte{'\n'})
					}
				}()
				wg.Wait()
				if received != tc.messageCount {
					t.Errorf("Expected %d messages, got %d", tc.messageCount, received)
				}
			})
		}
	})
}
