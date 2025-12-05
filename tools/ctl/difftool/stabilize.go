// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package difftool

import (
	"os"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/pkg/errors"
)

// stabilizeToFile stabilizes an artifact from input path to output path.
func stabilizeToFile(inputPath, outputPath string, target rebuild.Target, stabilizers []stabilize.Stabilizer) error {
	input, err := os.Open(inputPath)
	if err != nil {
		return errors.Wrap(err, "opening input")
	}
	defer input.Close()
	output, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE, os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "opening output")
	}
	defer output.Close()
	opts := stabilize.StabilizeOpts{}
	if len(stabilizers) > 0 {
		opts.Stabilizers = stabilizers
	}
	if err := stabilize.StabilizeWithOpts(output, input, target.ArchiveType(), opts); err != nil {
		return errors.Wrap(err, "running stabilize")
	}
	return nil
}
