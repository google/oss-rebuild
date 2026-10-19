// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"golang.org/x/sync/singleflight"
)

const (
	// snapshotWarmInterval is how often Warm looks for a new snapshot
	// version: half the snapshot cache's 30s poll, so Warm starts on a new
	// version at most 15s after the cache serves it.
	snapshotWarmInterval = 15 * time.Second
	// snapshotRefreshTimeout bounds a refresh that runs outside a request.
	snapshotRefreshTimeout = 2 * time.Minute
)

// SnapshotPages holds the data of pages that read nothing but the snapshot,
// each computed once per snapshot version and request. A page renders per
// request from a copy of its data, so the frame's live figures and relative
// times stay current.
type SnapshotPages struct {
	pages []warmer
}

// warmer is a page whose data Warm keeps current.
type warmer interface{ warm(context.Context, *Deps) }

// pageRequest is a page's request, which keys its data. Its zero value is
// the page's default view, which Warm computes up front.
type pageRequest interface {
	act.Input
	comparable
}

// PerSnapshot serves h's data from pages, computing it once per snapshot
// version. A request that finds its data stale is served that until Warm
// refreshes it. Without a snapshot h runs on every request. tabs, when set,
// lists the requests a page's default view links to, which Warm computes
// with it. Register every page before Warm starts.
func PerSnapshot[I pageRequest, O any](pages *SnapshotPages, h api.HandlerFn[I, O, *Deps], tabs func(*O) []I) api.HandlerFn[I, O, *Deps] {
	p := &snapshotPage[I, O]{h: h, tabs: tabs, data: map[I]snapshotData[O]{}}
	pages.pages = append(pages.pages, p)
	return p.serve
}

// Warm recomputes the data that predates the current snapshot version until
// ctx ends, so the requests after a snapshot update find their pages current.
// It computes every page's default request and its tabs up front. It needs
// a snapshot.
func (s *SnapshotPages) Warm(ctx context.Context, depsFn func(context.Context) (*Deps, error)) {
	deps, err := depsFn(ctx)
	if err != nil {
		log.Printf("warming snapshot pages: %v", err)
		return
	}
	t := time.NewTicker(snapshotWarmInterval)
	defer t.Stop()
	for {
		for _, p := range s.pages {
			p.warm(ctx, deps)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type snapshotPage[I pageRequest, O any] struct {
	h     api.HandlerFn[I, O, *Deps]
	tabs  func(*O) []I
	group singleflight.Group
	mu    sync.Mutex
	data  map[I]snapshotData[O]
}

type snapshotData[O any] struct {
	fresh time.Time // the snapshot version page was computed on
	page  *O
}

func (p *snapshotPage[I, O]) serve(ctx context.Context, req I, deps *Deps) (*O, error) {
	if deps.Analytics == nil {
		return p.h(ctx, req, deps)
	}
	p.mu.Lock()
	d, ok := p.data[req]
	p.mu.Unlock()
	if !ok {
		var err error
		if d, err = p.refresh(ctx, req, deps); err != nil {
			return nil, err
		}
	}
	page := *d.page
	return &page, nil
}

// refresh recomputes req's data unless it is current. Concurrent refreshes
// of a request share one run. The version is read before the run, so a
// snapshot that advances during it leaves the data stale for the next one.
func (p *snapshotPage[I, O]) refresh(ctx context.Context, req I, deps *Deps) (snapshotData[O], error) {
	v, err, _ := p.group.Do(fmt.Sprintf("%#v", req), func() (any, error) {
		fresh := deps.Analytics.Freshness()
		p.mu.Lock()
		d, ok := p.data[req]
		p.mu.Unlock()
		if ok && d.fresh.Equal(fresh) {
			return d, nil
		}
		page, err := p.h(ctx, req, deps)
		if err != nil {
			return nil, err
		}
		d = snapshotData[O]{fresh: fresh, page: page}
		p.mu.Lock()
		p.data[req] = d
		p.mu.Unlock()
		return d, nil
	})
	if err != nil {
		return snapshotData[O]{}, err
	}
	return v.(snapshotData[O]), nil
}

// warmOne refreshes req for Warm, logging a failure since no request waits
// on it.
func (p *snapshotPage[I, O]) warmOne(ctx context.Context, req I, deps *Deps) {
	ctx, cancel := context.WithTimeout(ctx, snapshotRefreshTimeout)
	defer cancel()
	if _, err := p.refresh(ctx, req, deps); err != nil {
		log.Printf("refreshing %#v: %v", req, err)
	}
}

// warm refreshes the default request, its tabs, and every request whose
// data predates the snapshot, one at a time. A refresh skips current data.
func (p *snapshotPage[I, O]) warm(ctx context.Context, deps *Deps) {
	var zero I
	p.warmOne(ctx, zero, deps)
	p.mu.Lock()
	reqs := slices.Collect(maps.Keys(p.data))
	if d, ok := p.data[zero]; ok && p.tabs != nil {
		reqs = append(reqs, p.tabs(d.page)...)
	}
	p.mu.Unlock()
	for _, req := range reqs {
		p.warmOne(ctx, req, deps)
	}
}
