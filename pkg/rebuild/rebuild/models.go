// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rebuild

import (
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
)

// Ecosystem represents a package ecosystem.
type Ecosystem string

// Ecosystem constants. These are used to select an ecosystem, and used as prefixes in storage.
const (
	NPM      Ecosystem = "npm"
	PyPI     Ecosystem = "pypi"
	CratesIO Ecosystem = "cratesio"
	Maven    Ecosystem = "maven"
)

// Target is a single target we might attempt to rebuild.
type Target struct {
	Ecosystem Ecosystem
	Package   string
	Version   string
	Artifact  string
}

// ArchiveType provide the Target's archive.Format.
func (t Target) ArchiveType() archive.Format {
	switch t.Ecosystem {
	case CratesIO, NPM:
		return archive.TarGzFormat
	case PyPI:
		switch {
		case strings.HasSuffix(t.Artifact, ".whl"), strings.HasSuffix(t.Artifact, ".zip"):
			return archive.ZipFormat
		case strings.HasSuffix(t.Artifact, ".tar.gz"):
			return archive.TarGzFormat
		// Deprecated in https://peps.python.org/pep-0715/
		case strings.HasSuffix(t.Artifact, ".egg"):
			return archive.ZipFormat
		// Deprecated in https://peps.python.org/pep-0527/
		case strings.HasSuffix(t.Artifact, ".tgz"), strings.HasSuffix(t.Artifact, ".tar.Z"):
			return archive.TarGzFormat
		case strings.HasSuffix(t.Artifact, ".tar"):
			return archive.TarFormat
		case strings.HasSuffix(t.Artifact, ".tar.bz2"), strings.HasSuffix(t.Artifact, ".tbz"):
			return archive.UnknownFormat // bzip2
		case strings.HasSuffix(t.Artifact, ".tar.xz"):
			return archive.UnknownFormat // xz
		default:
			return archive.UnknownFormat
		}
	case Maven:
		if strings.HasSuffix(t.Artifact, ".jar") {
			return archive.ZipFormat
		} else if strings.HasSuffix(t.Artifact, ".pom") {
			return archive.RawFormat
		}
		return archive.UnknownFormat
	default:
		return archive.UnknownFormat
	}
}

// Input is a request to rebuild a single target.
type Input struct {
	Target   Target
	Strategy Strategy
}

// Timings describe how long different sections of the rebuild took.
type Timings struct {
	CloneEstimate time.Duration
	Source        time.Duration
	Infer         time.Duration
	Build         time.Duration
}

func (t Timings) Total() time.Duration {
	return t.Source + t.Infer + t.Build
}

func (t Timings) EstimateCleanBuild() time.Duration {
	return t.CloneEstimate + t.Infer + t.Build
}

// Verdict is the result of a single rebuild attempt.
type Verdict struct {
	Target   Target
	Message  string
	Strategy Strategy
	Timings  Timings
}
