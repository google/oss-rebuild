// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type EnqueueDeps struct {
	Tracker  feed.Tracker
	Analyzer *url.URL
	Queue    taskqueue.Queue
}

// Enqueue sends an event to be analyzed.
func Enqueue(ctx context.Context, e schema.ReleaseEvent, deps *EnqueueDeps) (*api.NoReturn, error) {
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

// objectMetadata represents a subset of the core object metadata structure sent in the JSON_API_V1 payload format.
// Fields are based on the GCS Object resource: https://cloud.google.com/storage/docs/json_api/v1/objects#resource
type objectMetadata struct {
	Name       string     `json:"name"`        // The name of the object
	Bucket     string     `json:"bucket"`      // The name of the bucket containing this object
	Generation string     `json:"generation"`  // Use string as generation can be very large
	Created    *time.Time `json:"timeCreated"` // Object creation time
	Updated    *time.Time `json:"updated"`     // Last metadata update time
	Size       string     `json:"size"`        // Object size in bytes (as a string)
}

func RebuildMessageToReleaseEvent(body io.ReadCloser) (*schema.ReleaseEvent, error) {
	metadata := objectMetadata{}
	{
		if err := json.NewDecoder(body).Decode(&metadata); err != nil {
			return nil, errors.Wrap(err, "decoding metadata")
		}
		if err := body.Close(); err != nil {
			return nil, errors.Wrap(err, "closing request body")
		}
	}
	// Expected form: ecosystem/package/version/artifact/rebuild.intoto.jsonl
	// TODO: Use logic from AssetStore.
	parts := filepath.SplitList(metadata.Name)
	if len(parts) != 5 {
		return nil, errors.Errorf("unexpected object path length: %s", metadata.Name)
	}
	ecosystem, pkg, version, artifact, obj := parts[0], parts[1], parts[2], parts[3], parts[4]
	if obj != string(rebuild.AttestationBundleAsset) {
		return nil, errors.Errorf("unexpected object name: %s", obj)
	}
	return &schema.ReleaseEvent{
		Ecosystem: rebuild.Ecosystem(ecosystem),
		Package:   pkg,
		Version:   version,
		Artifact:  artifact,
	}, nil
}
