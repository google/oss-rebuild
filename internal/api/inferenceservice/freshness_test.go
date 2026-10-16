// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// publishedAt is a Rebuilder whose registry reports the given publish time.
type publishedAt struct {
	rebuild.Rebuilder
	at  time.Time
	err error
}

func (p publishedAt) Published(context.Context, rebuild.Target, rebuild.RegistryMux) (time.Time, error) {
	return p.at, p.err
}

func TestBoundCache(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, tc := range []struct {
		name      string
		rebuilder rebuild.Rebuilder
		want      func(now time.Time) time.Time
	}{
		{"Published", publishedAt{at: old}, func(time.Time) time.Time { return old.Add(publishLag) }},
		{"PublishedRecently", publishedAt{at: time.Now().Add(-time.Hour)}, func(now time.Time) time.Time { return now.Add(-minEntryAge) }},
		{"NoPublishTime", struct{ rebuild.Rebuilder }{}, func(now time.Time) time.Time { return now.Add(-defaultFreshness) }},
		{"PublishTimeUnknown", publishedAt{err: errors.New("no upload time")}, func(now time.Time) time.Time { return now.Add(-defaultFreshness) }},
		{"PublishTimeZero", publishedAt{}, func(now time.Time) time.Time { return now.Add(-defaultFreshness) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &gitcache.Client{}
			before := time.Now()
			ctx := boundCache(context.WithValue(t.Context(), rebuild.RepoCacheClientID, c), tc.rebuilder, rebuild.Target{}, rebuild.RegistryMux{})
			after := time.Now()
			got := ctx.Value(rebuild.RepoCacheClientID).(*gitcache.Client).DefaultFreshness
			if lo, hi := tc.want(before), tc.want(after); got.Before(lo) || got.After(hi) {
				t.Errorf("DefaultFreshness = %v, want %v", got, lo)
			}
			if !c.DefaultFreshness.IsZero() {
				t.Error("boundCache() changed the shared client")
			}
		})
	}
}

func TestBoundCacheWithoutCache(t *testing.T) {
	ctx := boundCache(t.Context(), publishedAt{at: time.Now()}, rebuild.Target{}, rebuild.RegistryMux{})
	if c := ctx.Value(rebuild.RepoCacheClientID); c != nil {
		t.Errorf("boundCache() added a cache client %v", c)
	}
}
