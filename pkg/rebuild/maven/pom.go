// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"encoding/xml"
	"fmt"

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
// TODO: NewPomXML should be added to the maven.Registry interface.
// TODO: This will ensure that we can simply resolve parent POM by only changing the target, thus eliminating the need for `ResolveParentPom`.
func NewPomXML(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (PomXML, error) {
	var p PomXML
	r, err := mux.Maven.ReleaseFile(ctx, t.Package, t.Version, maven.TypePOM)
	if err != nil {
		return p, err
	}
	defer r.Close()
	return p, xml.NewDecoder(r).Decode(&p)
}

func ResolveParentPom(ctx context.Context, pom PomXML, mux rebuild.RegistryMux) (PomXML, error) {
	if pom.Parent.ArtifactID == "" {
		return pom, nil
	}
	var ver, group string
	if pom.Parent.VersionID != "" {
		ver = pom.Parent.VersionID
	} else {
		ver = pom.VersionID
	}
	if pom.Parent.GroupID != "" {
		group = pom.Parent.GroupID
	} else {
		group = pom.GroupID
	}
	var parent PomXML
	r, err := mux.Maven.ReleaseFile(ctx, fmt.Sprintf("%s:%s", group, pom.Parent.ArtifactID), ver, maven.TypePOM)
	if err != nil {
		return parent, err
	}
	defer r.Close()
	return parent, xml.NewDecoder(r).Decode(&parent)
}
