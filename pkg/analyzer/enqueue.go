// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package analyzer provides common utilities for analyzer services.
package analyzer

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// GCSEventToTargetEvent converts GCS object event data to a TargetEvent.
// This is used by analyzer services to process GCS bucket notifications for attestation bundles.
func GCSEventToTargetEvent(event schema.GCSObjectEvent) (*schema.TargetEvent, error) {
	// Expected form: ecosystem/package/version/artifact/rebuild.intoto.jsonl
	// TODO: Use logic from AssetStore.
	parts := strings.Split(filepath.Clean(event.Name), "/")
	if len(parts) != 5 {
		return nil, errors.Errorf("unexpected object path length: %s", event.Name)
	}
	ecosystem, pkg, version, artifact, obj := parts[0], parts[1], parts[2], parts[3], parts[4]
	if obj != string(rebuild.AttestationBundleAsset) {
		return nil, errors.Errorf("unexpected object name: %s", obj)
	}
	return &schema.TargetEvent{
		Ecosystem: rebuild.Ecosystem(ecosystem),
		Package:   pkg,
		Version:   version,
		Artifact:  artifact,
	}, nil
}

// GCSEventBodyToTargetEvent converts an HTTP request body containing GCS object event data
// to a TargetEvent. This provides backward compatibility with the existing io.ReadCloser interface.
func GCSEventBodyToTargetEvent(body io.ReadCloser) (*schema.TargetEvent, error) {
	event := schema.GCSObjectEvent{}
	if err := json.NewDecoder(body).Decode(&event); err != nil {
		return nil, errors.Wrap(err, "decoding event")
	}
	if err := body.Close(); err != nil {
		return nil, errors.Wrap(err, "closing request body")
	}
	return GCSEventToTargetEvent(event)
}
