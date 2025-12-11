// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package analyzer provides common utilities for analyzer services.
package analyzer

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// GCSEventToTargetEvent converts GCS object event data to a TargetEvent.
// This is used by analyzer services to process GCS bucket notifications for attestation bundles.
func GCSEventToTargetEvent(event schema.GCSObjectEvent) (*schema.TargetEvent, error) {
	parts := strings.Split(event.Name, "/")
	if len(parts) != 5 {
		return nil, errors.Errorf("unexpected object path length: path=%s parts=%d", event.Name, len(parts))
	}
	ecosystem, pkg, version, artifact, obj := parts[0], parts[1], parts[2], parts[3], parts[4]
	t := rebuild.FilesystemTargetEncoding.New(rebuild.Ecosystem(ecosystem), pkg, version, artifact).Decode()
	if obj != string(rebuild.AttestationBundleAsset) {
		return nil, errors.Errorf("unexpected object name: %s", obj)
	}
	return &schema.TargetEvent{
		Ecosystem: t.Ecosystem,
		Package:   t.Package,
		Version:   t.Version,
		Artifact:  t.Artifact,
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
