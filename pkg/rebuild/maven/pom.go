// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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
	GroupID    string  `xml:"groupId"`
	ArtifactID string  `xml:"artifactId"`
	VersionID  string  `xml:"version"`
	URL        string  `xml:"url"`
	SCMURL     string  `xml:"scm>url"`
	Parent     *PomXML `xml:"parent"`
}

// Repo returns the repository URL for a Maven package.
func (p *PomXML) Repo() string {
	if p.SCMURL != "" {
		return p.SCMURL
	}
	if p.Parent != nil {
		return p.Parent.Repo()
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
		return p, err
	}
	defer r.Close()
	return p, xml.NewDecoder(r).Decode(&p)
}
