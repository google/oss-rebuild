// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"
	"io"
	"path"
	"regexp"

	"github.com/pkg/errors"
)

type CustomConfig interface {
	Stabilizer(name string, format Format) (Stabilizer, error)
	Validate() error
}

type CustomConfigOneOf struct {
	ReplacePattern *ReplacePattern `yaml:"replace_pattern"`
	ExcludePath    *ExcludePath    `yaml:"exclude_path"`
	Reason         string          `yaml:"reason"`
	FormatStr      string          `yaml:"format"`
}

func (cfg *CustomConfigOneOf) Format() *Format {
	var f Format
	switch cfg.FormatStr {
	case "tgz":
		f = TarGzFormat
	case "tar":
		f = TarFormat
	case "zip":
		f = ZipFormat
	case "":
		return nil
	default:
		f = UnknownFormat
	}
	return &f
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

func (cfg *CustomConfigOneOf) Validate() error {
	if cfg.Reason == "" {
		return errors.New("no reason provided")
	}
	if count(
		cfg.ReplacePattern != nil,
		cfg.ExcludePath != nil,
	) != 1 {
		return errors.New("exactly one CustomConfig must be set")
	}
	if f := cfg.Format(); f != nil && *f == UnknownFormat {
		return errors.New("unknown format")
	}
	return nil
}

func (cfg *CustomConfigOneOf) CustomConfig() CustomConfig {
	switch {
	case cfg.ReplacePattern != nil:
		return cfg.ReplacePattern
	case cfg.ExcludePath != nil:
		return cfg.ExcludePath
	default:
		return nil
	}
}

type ReplacePattern struct {
	Path    string `yaml:"path"`
	Pattern string `yaml:"pattern"`
	Replace string `yaml:"replace"`
}

func (rp *ReplacePattern) Validate() error {
	if rp.Path == "" {
		return errors.New("empty path")
	}
	if _, err := regexp.Compile(rp.Pattern); err != nil {
		return errors.Wrap(err, "bad pattern")
	}
	return nil
}

func (rp *ReplacePattern) Stabilizer(name string, format Format) (Stabilizer, error) {
	switch format {
	case TarGzFormat, TarFormat:
		return TarEntryStabilizer{
			Name: "replace-pattern-" + name,
			Func: func(te *TarEntry) {
				if match, err := path.Match(rp.Path, te.Name); err != nil || !match {
					return
				}
				re := regexp.MustCompile(rp.Pattern)
				te.Body = re.ReplaceAll(te.Body, []byte(rp.Replace))
			},
		}, nil
	case ZipFormat:
		return ZipEntryStabilizer{
			Name: "replace-pattern-" + name,
			Func: func(zf *MutableZipFile) {
				if match, err := path.Match(rp.Path, zf.Name); err != nil || !match {
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
				re := regexp.MustCompile(rp.Pattern)
				transformed := re.ReplaceAll(content, []byte(rp.Replace))
				zf.SetContent(transformed)
			},
		}, nil
	default:
		return nil, errors.New("unsupported format")
	}
}

type ExcludePath struct {
	Path string `yaml:"path"`
}

func (ep *ExcludePath) Validate() error {
	if ep.Path == "" {
		return errors.New("empty path")
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
					if match, err := path.Match(ep.Path, f.Name); err != nil || match {
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
					if match, err := path.Match(ep.Path, f.Name); err != nil || match {
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

// Convert CustomConfig specs to stabilizers
func CreateCustomStabilizers(configs []CustomConfigOneOf, defaultFormat Format) ([]Stabilizer, error) {
	var stabilizers []Stabilizer
	for i, cfg := range configs {
		if err := cfg.Validate(); err != nil {
			return nil, errors.Wrapf(err, "validating stabilizer config %d", i)
		}
		format := defaultFormat
		if f := cfg.Format(); f != nil {
			format = *f
		}
		name := fmt.Sprintf("custom%02d", i)
		if err := cfg.CustomConfig().Validate(); err != nil {
			return nil, errors.Wrapf(err, "validating stabilizer config %d", i)
		}
		stabilizer, err := cfg.CustomConfig().Stabilizer(name, format)
		if err != nil {
			return nil, errors.Wrapf(err, "creating stabilizer from config %d", i)
		}
		stabilizers = append(stabilizers, stabilizer)
	}
	return stabilizers, nil
}
