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

package maven

import (
	"context"
	"encoding/xml"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

// PomXML is the root element of a Maven POM file.
// The root element is called "project" and this is handled by the Decode method.
type PomXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	VersionID  string `xml:"version"`
	URL        string `xml:"url"`
	SCMURL     string `xml:"scm>url"`
	Parent     Parent `xml:"parent"`
}

// Parent represents the parent package ref within a Maven POM file.
type Parent struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	VersionID  string `xml:"version"`
}

// Repo returns the repository URL for a Maven package.
func (p *PomXML) Repo() string {
	if p.SCMURL != "" {
		return p.SCMURL
	}
	return p.URL
}

// Name returns the Maven package name.
func (p *PomXML) Name() string {
	return p.Group() + ":" + p.ArtifactID
}

// Group returns the Maven package group.
func (p *PomXML) Group() string {
	if g := p.GroupID; g != "" {
		return g
	}
	return p.Parent.GroupID
}

// Version returns the Maven package version.
func (p *PomXML) Version() string {
	if v := p.VersionID; v != "" {
		return v
	}
	return p.Parent.VersionID
}

// NewPomXML returns the POM file for a Maven package version.
func NewPomXML(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (p PomXML, err error) {
	var r io.ReadCloser
	r, err = mux.Maven.ReleaseFile(ctx, t.Package, t.Version, maven.TypePOM)
	if err != nil {
		return
	}
	defer r.Close()
	err = xml.NewDecoder(r).Decode(&p)
	return
}
