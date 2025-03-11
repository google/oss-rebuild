// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"
	"io"
	"regexp"

	"slices"

	"github.com/google/oss-rebuild/internal/glob"
	"github.com/pkg/errors"
)

// CustomStabilizerConfig defines a custom stabilizer that can be materialized for many formats
type CustomStabilizerConfig interface {
	Stabilizer(name string, format Format) (Stabilizer, error)
	Validate() error
}

// CustomStabilizerConfigOneOf aggregates known implementations of CustomStabilizerConfig
type CustomStabilizerConfigOneOf struct {
	ReplacePattern *ReplacePattern `yaml:"replace_pattern"`
	ExcludePath    *ExcludePath    `yaml:"exclude_path"`
}

func count(bools ...bool) int {
	var c int
	for _, b := range bools {
		if b {
			c++
		}
	}
	return c
}

func (cfg *CustomStabilizerConfigOneOf) Validate() error {
	if count(
		cfg.ReplacePattern != nil,
		cfg.ExcludePath != nil,
	) != 1 {
		return errors.New("exactly one config must be set")
	}
	return nil
}

func (cfg *CustomStabilizerConfigOneOf) CustomStabilizerConfig() CustomStabilizerConfig {
	switch {
	case cfg.ReplacePattern != nil:
		return cfg.ReplacePattern
	case cfg.ExcludePath != nil:
		return cfg.ExcludePath
	default:
		return nil
	}
}

// CustomStabilizerEntry defines a custom Stabilizer
type CustomStabilizerEntry struct {
	Config CustomStabilizerConfigOneOf `yaml:",inline"`
	Reason string                      `yaml:"reason"`
}

func (ent CustomStabilizerEntry) Validate() error {
	if ent.Reason == "" {
		return errors.New("no reason provided")
	}
	return ent.Config.Validate()
}

// CreateCustomStabilizers converts a set of CustomStabilizerEntry specs to Stabilizers.
// NOTE: This should only be called once. It generates stabilizer names using a
// 0-based integer counter so subsequent calls will generate identical names.
func CreateCustomStabilizers(entries []CustomStabilizerEntry, format Format) ([]Stabilizer, error) {
	var stabilizers []Stabilizer
	for i, ent := range entries {
		if err := ent.Validate(); err != nil {
			return nil, errors.Wrapf(err, "validating stabilizer config %d", i)
		}
		name := fmt.Sprintf("custom%02d", i)
		if err := ent.Config.CustomStabilizerConfig().Validate(); err != nil {
			return nil, errors.Wrapf(err, "validating stabilizer config %d", i)
		}
		stabilizer, err := ent.Config.CustomStabilizerConfig().Stabilizer(name, format)
		if err != nil {
			return nil, errors.Wrapf(err, "creating stabilizer from config %d", i)
		}
		stabilizers = append(stabilizers, stabilizer)
	}
	return stabilizers, nil
}

// ReplacePattern is a regex replace stabilizer applied to a specified path
// - Paths is a slice of path.Match-like patterns defining the archive paths to apply the exclusion.
// - Pattern is a regex that accepts the golang RE2 syntax.
// - Replace can define a substitution for the matched content.
type ReplacePattern struct {
	Paths   []string `yaml:"paths"`
	Pattern string   `yaml:"pattern"`
	Replace string   `yaml:"replace"`
}

func (rp *ReplacePattern) Validate() error {
	if len(rp.Paths) == 0 {
		return errors.New("no path provided")
	}
	if slices.Contains(rp.Paths, "") {
		return errors.New("invalid path")
	}
	if _, err := regexp.Compile(rp.Pattern); err != nil {
		return errors.Wrap(err, "bad pattern")
	}
	return nil
}

// Stabilizer materializes a Stabilizer for the given config, name, and format.
func (rp *ReplacePattern) Stabilizer(name string, format Format) (Stabilizer, error) {
	re := regexp.MustCompile(rp.Pattern)
	switch format {
	case TarGzFormat, TarFormat:
		return TarEntryStabilizer{
			Name: "replace-pattern-" + name,
			Func: func(te *TarEntry) {
				if match, err := multiMatch(rp.Paths, te.Name); err != nil || !match {
					return
				}
				te.Body = re.ReplaceAll(te.Body, []byte(rp.Replace))
				te.Size = int64(len(te.Body))
			},
		}, nil
	case ZipFormat:
		return ZipEntryStabilizer{
			Name: "replace-pattern-" + name,
			Func: func(zf *MutableZipFile) {
				if match, err := multiMatch(rp.Paths, zf.Name); err != nil || !match {
					return
				}
				r, err := zf.Open()
				if err != nil {
					return
				}
				content, err := io.ReadAll(r)
				if err != nil {
					return
				}
				transformed := re.ReplaceAll(content, []byte(rp.Replace))
				zf.SetContent(transformed)
			},
		}, nil
	default:
		return nil, errors.New("unsupported format")
	}
}

// ExcludePath is stabilizer that removes specified path(s) from the output
// - Paths is a slice of path.Match-like patterns defining the archive paths to apply the exclusion.
type ExcludePath struct {
	Paths []string `yaml:"paths"`
}

func (ep *ExcludePath) Validate() error {
	if len(ep.Paths) == 0 {
		return errors.New("no path provided")
	}
	if slices.Contains(ep.Paths, "") {
		return errors.New("invalid path")
	}
	return nil
}

func (ep *ExcludePath) Stabilizer(name string, format Format) (Stabilizer, error) {
	switch format {
	case TarGzFormat, TarFormat:
		return TarArchiveStabilizer{
			Name: "exclude-path-" + name,
			Func: func(ta *TarArchive) {
				var files []*TarEntry
				for _, f := range ta.Files {
					if match, err := multiMatch(ep.Paths, f.Name); err != nil || match {
						continue
					}
					files = append(files, f)
				}
				ta.Files = files
			},
		}, nil
	case ZipFormat:
		return ZipArchiveStabilizer{
			Name: "exclude-path-" + name,
			Func: func(mzr *MutableZipReader) {
				var files []*MutableZipFile
				for _, f := range mzr.File {
					if match, err := multiMatch(ep.Paths, f.Name); err != nil || match {
						continue
					}
					files = append(files, f)
				}
				mzr.File = files
			},
		}, nil
	default:
		return nil, errors.New("unsupported format")
	}
}

func multiMatch(patterns []string, name string) (bool, error) {
	for _, pattern := range patterns {
		if match, err := glob.Match(pattern, name); err != nil {
			return false, err
		} else if match {
			return true, nil
		}
	}
	return false, nil
}
