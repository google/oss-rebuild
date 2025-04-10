// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stability

import (
	"slices"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// StabilizersForTarget returns the appropriate stabilizers for a given target.
func StabilizersForTarget(t rebuild.Target) ([]archive.Stabilizer, error) {
	format := t.ArchiveType()
	if format == archive.UnknownFormat {
		return nil, errors.Errorf("unknown archive format for %s %s", t.Ecosystem, t.Artifact)
	}
	var stabilizers []archive.Stabilizer
	switch format {
	case archive.ZipFormat:
		stabilizers = slices.Clone(archive.AllZipStabilizers)
	case archive.TarFormat:
		stabilizers = slices.Clone(archive.AllTarStabilizers)
	case archive.TarGzFormat:
		stabilizers = slices.Concat(archive.AllTarStabilizers, archive.AllGzipStabilizers)
	}
	switch t.Ecosystem {
	case rebuild.Maven:
		if format == archive.ZipFormat {
			stabilizers = append(stabilizers, archive.AllJarStabilizers...)
		}
	}
	return stabilizers, nil
}
