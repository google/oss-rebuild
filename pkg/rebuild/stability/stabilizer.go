// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stability

import (
	"slices"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/target"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/pkg/errors"
)

// StabilizersForTarget returns the appropriate stabilizers for a given target.
func StabilizersForTarget(t target.Target) ([]stabilize.Stabilizer, error) {
	format := t.ArchiveType()
	if format == archive.UnknownFormat {
		return nil, errors.Errorf("unknown archive format for %s %s", t.Ecosystem, t.Artifact)
	}
	var stabilizers []stabilize.Stabilizer
	switch format {
	case archive.ZipFormat:
		stabilizers = slices.Clone(stabilize.AllZipStabilizers)
	case archive.TarFormat:
		stabilizers = slices.Clone(stabilize.AllTarStabilizers)
	case archive.TarGzFormat:
		stabilizers = slices.Concat(stabilize.AllTarStabilizers, stabilize.AllGzipStabilizers)
	}
	switch t.Ecosystem {
	case target.PyPI:
		if format == archive.ZipFormat {
			stabilizers = append(stabilizers, stabilize.AllWheelStabilizers...)
		}
	case target.Maven:
		if format == archive.ZipFormat {
			stabilizers = append(stabilizers, stabilize.AllJarStabilizers...)
		}
	case target.CratesIO:
		if format == archive.TarGzFormat {
			stabilizers = append(stabilizers, stabilize.AllCrateStabilizers...)
		}
	case target.RubyGems:
		if format == archive.TarFormat {
			stabilizers = append(stabilizers, stabilize.AllGemStabilizers...)
			// NOTE: Include gz stabilization for .gem inner archives like data.tar.gz and metadata.gz.
			stabilizers = append(stabilizers, stabilize.AllGzipStabilizers...)
		}
	}
	return stabilizers, nil
}
