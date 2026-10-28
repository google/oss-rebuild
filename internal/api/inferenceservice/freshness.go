// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

const (
	publishLag       = 3 * 24 * time.Hour // a release's tags and commits can trail its publish
	defaultFreshness = 24 * time.Hour     // for a target without a publish time
	minEntryAge      = time.Hour          // an entry this fresh serves any bound
)

// boundCache has the git cache serve an entry fetched publishLag after t's
// artifact was published, or within defaultFreshness when t has no publish
// time. The bound never passes minEntryAge ago, so requests soon after a
// publish share an entry rather than each refilling it.
func boundCache(ctx context.Context, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux) context.Context {
	c, _ := ctx.Value(rebuild.RepoCacheClientID).(*gitcache.Client)
	if c == nil {
		return ctx
	}
	now := time.Now()
	bound := now.Add(-defaultFreshness)
	if p, ok := rebuilder.(rebuild.Publisher); ok {
		switch published, err := p.Published(ctx, t, mux); {
		case err != nil:
			log.Printf("Publish time unknown [pkg=%s, version=%v]: %v", t.Package, t.Version, err)
		case published.IsZero():
			log.Printf("Publish time unknown [pkg=%s, version=%v]", t.Package, t.Version)
		default:
			bound = published.Add(publishLag)
		}
	}
	if latest := now.Add(-minEntryAge); bound.After(latest) {
		bound = latest
	}
	bounded := *c
	bounded.DefaultFreshness = bound
	return context.WithValue(ctx, rebuild.RepoCacheClientID, &bounded)
}
