// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"log"
	"slices"
	"sync"
	"time"
)

// listTimeout bounds a refresh, which every concurrent Get waits on.
const listTimeout = 30 * time.Second

// SnapshotList caches the archived snapshot listing, which changes every few
// weeks. A failed refresh serves the last listing for another TTL.
type SnapshotList struct {
	TTL  time.Duration
	List func(context.Context) ([]string, error)

	mu       sync.Mutex
	listing  []string
	listedAt time.Time
}

// Get returns the snapshot dates, oldest first.
func (l *SnapshotList) Get(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listing != nil && time.Since(l.listedAt) < l.TTL {
		return slices.Clone(l.listing), nil
	}
	// The refresh serves every waiting Get, so it outlives its caller.
	listCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listTimeout)
	defer cancel()
	listing, err := l.List(listCtx)
	if err != nil {
		if l.listing == nil {
			return nil, err
		}
		log.Printf("refreshing the snapshot listing, serving the last one: %v", err)
		l.listedAt = time.Now()
		return slices.Clone(l.listing), nil
	}
	l.listing, l.listedAt = listing, time.Now()
	return slices.Clone(listing), nil
}
