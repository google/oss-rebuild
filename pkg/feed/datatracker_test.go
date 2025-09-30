// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// resetGlobalCache clears the shared index to ensure test isolation.
func resetGlobalCache() {
	sharedIndexesMu.Lock()
	defer sharedIndexesMu.Unlock()
	sharedIndexes = nil
}

// newGzippedPayload is a test helper to create a reader with gzipped JSON content.
func newGzippedPayload(t *testing.T, payload any) io.ReadCloser {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gzw).Encode(payload); err != nil {
		t.Fatalf("Failed to create test payload: %v", err)
	}
	gzw.Close()
	return io.NopCloser(&buf)
}

type mockObject struct {
	ident     string
	version   int64
	NewReader func(ctx context.Context, version int64) (io.ReadCloser, error)
}

func (m mockObject) GetReader(ctx context.Context, version int64) (io.ReadCloser, error) {
	return m.NewReader(ctx, version)
}
func (m mockObject) GetCurrentVersion(ctx context.Context) (int64, error) {
	return m.version, nil
}
func (m mockObject) GetIdentifier() string {
	return m.ident
}

// TestNewTracker tests the generic NewTracker function.
func TestNewTracker(t *testing.T) {
	ctx := context.Background()

	testPackages := TrackedPackageSet{
		"npm":  {"react", "lodash"},
		"pypi": {"requests"},
	}

	t.Run("Track existing packages", func(t *testing.T) {
		resetGlobalCache()

		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				return newGzippedPayload(t, testPackages), nil
			},
		}

		tracker, err := NewTracker(ctx, mockObj, 1)
		if err != nil {
			t.Fatalf("NewTracker() error = %v, wantErr nil", err)
		}

		// Wait for the background goroutine to finish loading the index.
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready

		if sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err != nil {
			t.Fatalf("Index loading returned an unexpected error: %v", sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err)
		}

		// Test a tracked npm package
		tracked, err := tracker.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "react"})
		if err != nil {
			t.Errorf("tracker() returned an error: %v", err)
		}
		if !tracked {
			t.Error("Expected to track 'npm/react', but it was not tracked")
		}

		// Test another tracked npm package
		tracked, err = tracker.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "lodash"})
		if err != nil {
			t.Errorf("tracker() returned an error: %v", err)
		}
		if !tracked {
			t.Error("Expected to track 'npm/lodash', but it was not tracked")
		}

		// Test a tracked pypi package
		tracked, err = tracker.IsTracked(schema.TargetEvent{Ecosystem: "pypi", Package: "requests"})
		if err != nil {
			t.Errorf("tracker() returned an error: %v", err)
		}
		if !tracked {
			t.Error("Expected to track 'pypi/requests', but it was not tracked")
		}

		// Test an untracked package
		untracked, err := tracker.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "angular"})
		if err != nil {
			t.Errorf("tracker() returned an error: %v", err)
		}
		if untracked {
			t.Error("Expected not to track 'npm/angular', but it was tracked")
		}
	})

	t.Run("Caching - Should fetch only once for same generation", func(t *testing.T) {
		resetGlobalCache()
		var callCount int32

		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				atomic.AddInt32(&callCount, 1)
				return newGzippedPayload(t, testPackages), nil
			},
		}

		// First call - should trigger fetch
		if _, err := NewTracker(ctx, mockObj, 1); err != nil {
			t.Fatalf("First call to NewTracker() failed: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready // Wait for fetch

		if got := atomic.LoadInt32(&callCount); got != 1 {
			t.Errorf("NewReader calls = %d, want 1", got)
		}

		// Second call with same generation - should use cache
		if _, err := NewTracker(ctx, mockObj, 1); err != nil {
			t.Fatalf("Second call to NewTracker() failed: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready // Wait for fetch

		if got := atomic.LoadInt32(&callCount); got != 1 {
			t.Errorf("cached call count = %d, want 1", got)
		}

		// Third call with new generation - should fetch again
		mockObj.version = 2
		if _, err := NewTracker(ctx, mockObj, 2); err != nil {
			t.Fatalf("Third call to NewTracker() failed: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready // Wait for fetch

		if got := atomic.LoadInt32(&callCount); got != 2 {
			t.Errorf("call count after generation change = %d, want 2", got)
		}
	})

	t.Run("Caching - Different objects have different cache", func(t *testing.T) {
		resetGlobalCache()

		mockObj1 := mockObject{
			ident:   "test-bucket/test-object1",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				return newGzippedPayload(t, TrackedPackageSet{
					"npm": {"react"},
				}), nil
			},
		}
		mockObj2 := mockObject{
			ident:   "test-bucket/test-object2",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				return newGzippedPayload(t, TrackedPackageSet{
					"npm": {"lodash"},
				}), nil
			},
		}

		tracker1, err := NewTracker(ctx, mockObj1, 1)
		if err != nil {
			t.Fatalf("First call to NewTracker() failed: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object1"].(*VersionedIndex[int64]).Ready // Wait for fetch

		tracker2, err := NewTracker(ctx, mockObj2, 1)
		if err != nil {
			t.Fatalf("Second call to NewTracker() failed: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object2"].(*VersionedIndex[int64]).Ready // Wait for fetch

		if sharedIndexes["test-bucket/test-object1"].(*VersionedIndex[int64]).Err != nil {
			t.Fatalf("First index loading: %v", sharedIndexes["test-bucket/test-object1"].(*VersionedIndex[int64]).Err)
		}
		if sharedIndexes["test-bucket/test-object2"].(*VersionedIndex[int64]).Err != nil {
			t.Fatalf("Second index loading: %v", sharedIndexes["test-bucket/test-object2"].(*VersionedIndex[int64]).Err)
		}

		// Tracker1 expects react but not lodash to be tracked.
		if tracked, err := tracker1.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "react"}); err != nil {
			t.Errorf("tracker1 returned an error: %v", err)
		} else if !tracked {
			t.Errorf("Expected tracker1 to track 'npm/react', but it was not tracked")
		}
		if tracked, err := tracker1.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "lodash"}); err != nil {
			t.Errorf("tracker1 returned an error: %v", err)
		} else if tracked {
			t.Errorf("Expected tracker1 not to track 'npm/lodash', but it was tracked")
		}

		// Tracker2 expects lodash but not react to be tracked.
		if tracked, err := tracker2.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "lodash"}); err != nil {
			t.Errorf("tracker2 returned an error: %v", err)
		} else if !tracked {
			t.Errorf("Expected tracker2 to track 'npm/lodash', but it was not tracked")
		}
		if tracked, err := tracker2.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "react"}); err != nil {
			t.Errorf("tracker2 returned an error: %v", err)
		} else if tracked {
			t.Errorf("Expected tracker2 not to track 'npm/react', but it was tracked")
		}

	})

	t.Run("Concurrency - Multiple requests should only fetch once", func(t *testing.T) {
		resetGlobalCache()
		var callCount int32
		var wg sync.WaitGroup
		numRoutines := 10

		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				atomic.AddInt32(&callCount, 1)
				return newGzippedPayload(t, testPackages), nil
			},
		}

		for i := 0; i < numRoutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := NewTracker(ctx, mockObj, 1); err != nil {
					t.Errorf("Concurrent NewTracker() call failed: %v", err)
				}
			}()
		}
		wg.Wait()
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready // Wait for the single fetch to complete

		if got := atomic.LoadInt32(&callCount); got != 1 {
			t.Errorf("concurrent NewReader calls = %d, want 1", got)
		}
	})

	t.Run("Error Handling - GCS reader error", func(t *testing.T) {
		resetGlobalCache()
		expectedErr := errors.New("simulated GCS error")
		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				return nil, expectedErr
			},
		}
		tracker, err := NewTracker(ctx, mockObj, 1)
		if err != nil {
			t.Fatalf("NewTracker setup returned an error: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready // Wait for goroutine

		if sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err == nil {
			t.Fatal("Expected an error from GCS reader, but got nil")
		}
		if !errors.Is(sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err, expectedErr) {
			t.Errorf("Error mismatch: got %v, want %v", sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err, expectedErr)
		}
		_, err = tracker.IsTracked(schema.TargetEvent{Ecosystem: "npm", Package: "react"})
		if !errors.Is(err, expectedErr) {
			t.Errorf("Error mismatch: got %v, want %v", sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err, expectedErr)
		}
	})

	t.Run("Error Handling - Invalid Gzip data", func(t *testing.T) {
		resetGlobalCache()
		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				// Return plain text instead of gzipped data
				return io.NopCloser(bytes.NewReader([]byte("this is not gzipped"))), nil
			},
		}

		if _, err := NewTracker(ctx, mockObj, 1); err != nil {
			t.Fatalf("NewTracker setup returned an error: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready

		if sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err == nil {
			t.Fatal("Expected an error from gzip reader, but got nil")
		}
	})

	t.Run("Error Handling - Invalid JSON data", func(t *testing.T) {
		resetGlobalCache()
		mockObj := mockObject{
			ident:   "test-bucket/test-object",
			version: 1,
			NewReader: func(ctx context.Context, _ int64) (io.ReadCloser, error) {
				return newGzippedPayload(t, "{not valid json"), nil
			},
		}

		if _, err := NewTracker(ctx, mockObj, 1); err != nil {
			t.Fatalf("NewTracker setup returned an error: %v", err)
		}
		<-sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Ready

		if sharedIndexes["test-bucket/test-object"].(*VersionedIndex[int64]).Err == nil {
			t.Fatal("Expected an error from json decoder, but got nil")
		}
	})
}
