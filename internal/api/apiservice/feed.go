// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/feed"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// FeedDeps binds feed admission to the queue and the published signals.
type FeedDeps struct {
	Firestore *firestore.Client
	Signals   billy.Basic // signal database publish dir
	Registry  rebuild.RegistryMux
}

// Feed admits one batch of published releases into the onboarding queue.
func Feed(ctx context.Context, req feed.Request, deps *FeedDeps) (*feed.Summary, error) {
	cfg, err := req.Config()
	if err != nil {
		return nil, err
	}
	sdb, cleanup, err := signals.Open(deps.Signals)
	if err != nil {
		return nil, errors.Wrap(err, "opening signal database")
	}
	defer cleanup()
	p := &feed.Pass{
		Campaigns: db.NewFirestoreCampaigns(deps.Firestore),
		Signals:   sdb,
		Registry:  deps.Registry,
		Config:    cfg,
		Err:       log.Writer(),
		Now:       time.Now().UTC(),
	}
	summary, err := p.Run(ctx, req.Events)
	if err != nil {
		return nil, err
	}
	log.Println(summary)
	return summary, nil
}
