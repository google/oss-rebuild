// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// holds reports whether exactly n bytes of m are held.
func holds(m *Memory, n int64) bool {
	free := m.capacity - n
	if !m.sem.TryAcquire(free) {
		return false
	}
	defer m.sem.Release(free)
	if m.sem.TryAcquire(1) {
		m.sem.Release(1)
		return false
	}
	return true
}

func TestMemoryReserve(t *testing.T) {
	if NewMemory(baseMemory) != nil {
		t.Error("NewMemory() within base memory returned a Memory")
	}
	for _, tc := range []struct {
		name string
		need int64
		want int64 // bytes held, -1 for the whole capacity
	}{
		{"Exempt", exemptThreshold, 0},
		{"Held", 1 << 30, 1 << 30},
		{"OverCapacity", 1 << 40, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMemory(baseMemory + 4<<30)
			want := tc.want
			if want < 0 {
				want = m.capacity
			}
			release, err := m.reserve(t.Context(), tc.need)
			if err != nil {
				t.Fatalf("reserve() error = %v", err)
			}
			if !holds(m, want) {
				t.Errorf("reserve() holds other than %d bytes", want)
			}
			release()
			if !holds(m, 0) {
				t.Error("release() left memory held")
			}
		})
	}
}

func TestMemoryReserveWaitsForRoom(t *testing.T) {
	m := NewMemory(baseMemory + 1<<30)
	release, err := m.reserve(t.Context(), 1<<40)
	if err != nil {
		t.Fatalf("reserve() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.reserve(ctx, 512<<20); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reserve() beside a full instance: err = %v, want DeadlineExceeded", err)
	}
	release()
	second, err := m.reserve(t.Context(), 512<<20)
	if err != nil {
		t.Fatalf("reserve() once room frees: err = %v", err)
	}
	second()
}

// upstreamAt is a Rebuilder whose upstream artifact is at its URL.
type upstreamAt struct {
	rebuild.Rebuilder
	url string
}

func (u upstreamAt) UpstreamURL(context.Context, rebuild.Target, rebuild.RegistryMux) (string, error) {
	return u.url, nil
}

func TestReserveMemorySizes(t *testing.T) {
	const repo = "https://github.com/org/repo"
	const archive, artifact = 100 << 20, 50 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get":
			w.Header().Set("Git-Cache-Size", strconv.Itoa(archive))
			http.Redirect(w, r, "/archive", http.StatusFound)
		case "/artifact":
			if r.Method != http.MethodHead {
				t.Errorf("artifact fetched with %s, want HEAD", r.Method)
			}
			w.Header().Set("Content-Length", strconv.Itoa(artifact))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		artifactPath string
		maxBytes     int64 // the git cache client's fetch budget
		wantHeld     int64
		wantTooLarge *gitx.RepoTooLargeError
	}{
		{name: "RepositoryAndArtifact", artifactPath: "/artifact", wantHeld: repoFactor*archive + artifactFactor*artifact},
		{name: "UnsizedArtifact", artifactPath: "/missing", wantHeld: repoFactor * archive},
		{name: "OversizeRepository", artifactPath: "/artifact", maxBytes: archive - 1, wantTooLarge: &gitx.RepoTooLargeError{URL: repo, Limit: archive - 1, Size: archive}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMemory(baseMemory + 4<<30)
			deps := &InferDeps{HTTPClient: srv.Client(), Memory: m}
			cache := &gitcache.Client{IDClient: srv.Client(), APIClient: srv.Client(), URL: u, MaxBytes: tc.maxBytes}
			ctx := context.WithValue(t.Context(), rebuild.RepoCacheClientID, cache)
			release, err := reserveMemory(ctx, deps, upstreamAt{url: srv.URL + tc.artifactPath}, rebuild.Target{}, rebuild.RegistryMux{}, repo)
			defer release()
			var tooLarge *gitx.RepoTooLargeError
			if err != nil && !errors.As(err, &tooLarge) {
				t.Fatalf("reserveMemory() error = %v", err)
			}
			if diff := cmp.Diff(tc.wantTooLarge, tooLarge); diff != "" {
				t.Errorf("reserveMemory() refusal mismatch (-want +got):\n%s", diff)
			}
			if !holds(m, tc.wantHeld) {
				t.Errorf("reserveMemory() holds other than %d bytes", tc.wantHeld)
			}
		})
	}
}
