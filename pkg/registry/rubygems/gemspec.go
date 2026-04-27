// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"archive/tar"
	"compress/gzip"
	"io"

	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// GemSpec is the gem specification embedded as YAML in metadata.gz within
// a .gem file. Only fields needed for rebuild inference are included.
type GemSpec struct {
	RubygemsVersion string `yaml:"rubygems_version"`
}

// ParseGemSpec extracts the gem specification from a .gem file reader.
// The .gem file is a tar archive (not gzipped) containing metadata.gz,
// data.tar.gz, and checksums.yaml.gz. The metadata.gz entry is a
// gzip-compressed YAML gem specification.
func ParseGemSpec(r io.Reader) (*GemSpec, error) {
	tr := tar.NewReader(r)
	for header, err := range iterx.ToSeq2(tr, io.EOF) {
		if err != nil {
			return nil, errors.Wrap(err, "reading gem tar")
		}
		if header.Name != "metadata.gz" {
			continue
		}
		gz, err := gzip.NewReader(tr)
		if err != nil {
			return nil, errors.Wrap(err, "decompressing metadata.gz")
		}
		defer gz.Close()
		var spec GemSpec
		if err := yaml.NewDecoder(gz).Decode(&spec); err != nil {
			return nil, errors.Wrap(err, "parsing gem metadata YAML")
		}
		return &spec, nil
	}
	return nil, errors.New("metadata.gz not found in gem")
}
