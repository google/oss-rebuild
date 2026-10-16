// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"context"
	"net/url"

	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type EnqueueDeps struct {
	Skip     func(schema.TargetEvent) bool // drops events the analyzer ignores
	Analyzer *url.URL
	Queue    taskqueue.Queue
}

// Enqueue sends an event to be analyzed.
func Enqueue(ctx context.Context, e schema.TargetEvent, deps *EnqueueDeps) (*api.NoOutput, error) {
	if deps.Skip != nil && deps.Skip(e) {
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
