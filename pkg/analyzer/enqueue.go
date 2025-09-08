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
	if len(parts) < 5 {
		return nil, errors.Errorf("unexpected object path length: path=%s parts=%d", event.Name, len(parts))
	}
	var ecosystem, pkg, version, artifact, obj string
	switch rebuild.Ecosystem(parts[0]) {
	case rebuild.NPM:
		if len(parts) == 6 && !strings.HasPrefix(parts[1], "@") {
			// Assert pkgscope has a @ prefix
			return nil, errors.Errorf("unexpected package scope for scoped object path: path=%s scope=%s", event.Name, parts[1])
		}
		fallthrough
	case rebuild.Debian:
		if len(parts) == 6 {
			// Format: ecosystem/pkgscope/package/version/artifact/rebuild.intoto.jsonl
			ecosystem, pkg, version, artifact, obj = parts[0], parts[1]+"/"+parts[2], parts[3], parts[4], parts[5]
			break
		} else if len(parts) != 5 {
			return nil, errors.Errorf("unexpected object path length: path=%s parts=%d", event.Name, len(parts))
		}
		fallthrough
	case rebuild.CratesIO, rebuild.PyPI, rebuild.Maven:
		// Format: ecosystem/package/version/artifact/rebuild.intoto.jsonl
		ecosystem, pkg, version, artifact, obj = parts[0], parts[1], parts[2], parts[3], parts[4]
	default:
		return nil, errors.Errorf("unexpected ecosystem: '%s'", event.Name)
	}
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
