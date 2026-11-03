// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// versionedAnalytics is a snapshot whose version a test advances.
type versionedAnalytics struct {
	mu    sync.Mutex
	fresh time.Time
}

func (v *versionedAnalytics) Query(func(*sqlite3.Conn) error) error {
	return errors.New("no database")
}

func (v *versionedAnalytics) Freshness() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.fresh
}

func (v *versionedAnalytics) advance() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.fresh = v.fresh.Add(time.Minute)
}

type testPageReq struct{ Eco string }

func (testPageReq) Validate() error { return nil }

type testPage struct{ Run int64 }

// countingPage returns the run number it computed and the count of runs.
func countingPage(fail *atomic.Bool) (func(context.Context, testPageReq, *Deps) (*testPage, error), *atomic.Int64) {
	var runs atomic.Int64
	return func(context.Context, testPageReq, *Deps) (*testPage, error) {
		n := runs.Add(1)
		if fail != nil && fail.Load() {
			return nil, errors.New("query failed")
		}
		return &testPage{Run: n}, nil
	}, &runs
}

func TestPerSnapshotComputesOncePerVersion(t *testing.T) {
	ctx := context.Background()
	a := &versionedAnalytics{fresh: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
	deps := &Deps{Analytics: a}
	h, runs := countingPage(nil)
	serve := PerSnapshot(&SnapshotPages{}, h, nil)
	first, err := serve(ctx, testPageReq{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	// A copy goes out, so a caller's edits stay out of the cache.
	first.Run = 99
	second, err := serve(ctx, testPageReq{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run != 1 || runs.Load() != 1 {
		t.Errorf("second serve = run %d after %d runs, want run 1 after 1", second.Run, runs.Load())
	}
	// Another request has its own data.
	if npm, _ := serve(ctx, testPageReq{Eco: "npm"}, deps); npm.Run != 2 {
		t.Errorf("npm serve = run %d, want 2", npm.Run)
	}
	// A new version serves the stale data until Warm refreshes it.
	a.advance()
	if stale, _ := serve(ctx, testPageReq{}, deps); stale.Run != 1 || runs.Load() != 2 {
		t.Errorf("serve after the update = run %d after %d runs, want the stale run 1 after 2", stale.Run, runs.Load())
	}
}

func TestPerSnapshotDoesNotKeepErrors(t *testing.T) {
	ctx := context.Background()
	deps := &Deps{Analytics: &versionedAnalytics{}}
	var fail atomic.Bool
	fail.Store(true)
	h, runs := countingPage(&fail)
	serve := PerSnapshot(&SnapshotPages{}, h, nil)
	if _, err := serve(ctx, testPageReq{}, deps); err == nil {
		t.Fatal("serve succeeded through a failing query")
	}
	fail.Store(false)
	if got, err := serve(ctx, testPageReq{}, deps); err != nil || got.Run != 2 || runs.Load() != 2 {
		t.Errorf("serve after the failure = %+v, %v after %d runs, want run 2", got, err, runs.Load())
	}
}

func TestPerSnapshotWithoutSnapshotComputesEachTime(t *testing.T) {
	h, runs := countingPage(nil)
	serve := PerSnapshot(&SnapshotPages{}, h, nil)
	for range 2 {
		if _, err := serve(context.Background(), testPageReq{}, &Deps{}); err != nil {
			t.Fatal(err)
		}
	}
	if runs.Load() != 2 {
		t.Errorf("%d runs, want one per request", runs.Load())
	}
}

func TestSnapshotPagesWarm(t *testing.T) {
	ctx := context.Background()
	a := &versionedAnalytics{}
	deps := &Deps{Analytics: a}
	pages := &SnapshotPages{}
	h, runs := countingPage(nil)
	serve := PerSnapshot(pages, h, nil)
	warm := func() {
		for _, p := range pages.pages {
			p.warm(ctx, deps)
		}
	}
	// The default request is computed before anyone asks.
	warm()
	if runs.Load() != 1 {
		t.Fatalf("%d runs after the first warm, want the default request's", runs.Load())
	}
	if _, err := serve(ctx, testPageReq{Eco: "npm"}, deps); err != nil {
		t.Fatal(err)
	}
	// Nothing to do until the version moves, then every request refreshes.
	warm()
	if runs.Load() != 2 {
		t.Fatalf("%d runs after an idle warm, want 2", runs.Load())
	}
	a.advance()
	warm()
	if runs.Load() != 4 {
		t.Errorf("%d runs after a warm on a new version, want 4", runs.Load())
	}
	if got, _ := serve(ctx, testPageReq{}, deps); got.Run <= 2 {
		t.Errorf("serve after the warm = run %d, want one on the new version", got.Run)
	}
}

func TestSnapshotPagesWarmTabs(t *testing.T) {
	ctx := context.Background()
	deps := &Deps{Analytics: &versionedAnalytics{}}
	pages := &SnapshotPages{}
	var seen sync.Map
	h := func(_ context.Context, req testPageReq, _ *Deps) (*testPage, error) {
		seen.Store(req.Eco, true)
		return &testPage{}, nil
	}
	PerSnapshot(pages, h, func(*testPage) []testPageReq { return []testPageReq{{Eco: "npm"}, {Eco: "pypi"}} })
	for _, p := range pages.pages {
		p.warm(ctx, deps)
	}
	for _, eco := range []string{"", "npm", "pypi"} {
		if _, ok := seen.Load(eco); !ok {
			t.Errorf("warm skipped tab %q", eco)
		}
	}
}

func TestSnapshotMemo(t *testing.T) {
	var m snapshotMemo[int]
	var runs int
	var fail bool
	load := func(Analytics) (int, error) {
		runs++
		if fail {
			return 0, errors.New("query failed")
		}
		return runs, nil
	}
	a, b := &versionedAnalytics{}, &versionedAnalytics{}
	for _, step := range []struct {
		name string
		a    *versionedAnalytics
		want int
	}{
		{"first load", a, 1},
		{"same version", a, 1},
		{"another snapshot", b, 2},
		{"back to the first", a, 3},
	} {
		if got, err := m.get(step.a, load); err != nil || got != step.want {
			t.Errorf("%s: get = %d, %v, want %d", step.name, got, err, step.want)
		}
	}
	a.advance()
	fail = true
	if _, err := m.get(a, load); err == nil {
		t.Error("get on a new version kept the old result through a failing load")
	}
	fail = false
	if got, _ := m.get(a, load); got != 5 {
		t.Errorf("get after the failure = %d, want a fresh load, 5", got)
	}
}
