// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"context"
	"net/url"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type EnqueueDeps struct {
	Tracker  feed.Tracker
	Analyzer *url.URL
	Queue    taskqueue.Queue
}

// Enqueue sends an event to be analyzed.
func Enqueue(ctx context.Context, e schema.TargetEvent, deps *EnqueueDeps) (*api.NoReturn, error) {
	if tracked, err := deps.Tracker.IsTracked(e); err != nil {
		return nil, errors.Wrap(err, "checking if tracked")
	} else if !tracked {
		return nil, nil
	}
	msg := schema.AnalyzeRebuildRequest{
		Ecosystem: e.Ecosystem,
		Package:   e.Package,
		Version:   e.Version,
		Artifact:  e.Artifact,
	}
	_, err := deps.Queue.Add(ctx, deps.Analyzer.JoinPath("analyze").String(), msg)
	return nil, err
}
