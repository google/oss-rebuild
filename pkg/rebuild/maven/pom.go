// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

// PomXML is the root element of a Maven POM file.
// The root element is called "project" and this is handled by the Decode method.
type PomXML struct {
	GroupID                string        `xml:"groupId"`
	ArtifactID             string        `xml:"artifactId"`
	VersionID              string        `xml:"version"`
	URL                    string        `xml:"url"`
	SCMURL                 string        `xml:"scm>url"`
	SCMConnection          string        `xml:"scm>connection"`
	SCMDeveloperConnection string        `xml:"scm>developerConnection"`
	Properties             pomProperties `xml:"properties"`
	Parent                 Parent        `xml:"parent"`
}

// pomProperties are a POM's <properties>, by element name.
type pomProperties map[string]string

func (p *pomProperties) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = pomProperties{}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			var v string
			if err := d.DecodeElement(&v, &e); err != nil {
				return err
			}
			(*p)[e.Name.Local] = strings.TrimSpace(v)
		case xml.EndElement:
			return nil
		}
	}
}

func (p pomProperties) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(p) == 0 {
		return nil
	}
	names := slices.Sorted(maps.Keys(p))
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, name := range names {
		if err := e.EncodeElement(p[name], xml.StartElement{Name: xml.Name{Local: name}}); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

var pomPropertyRE = regexp.MustCompile(`\$\{([^}]+)\}`)

// Interpolate expands the ${...} references in s that the POM defines.
func (p *PomXML) Interpolate(s string) string {
	return pomPropertyRE.ReplaceAllStringFunc(s, func(ref string) string {
		name := ref[2 : len(ref)-1]
		switch name {
		case "project.groupId", "pom.groupId":
			return p.Group()
		case "project.artifactId", "pom.artifactId":
			return p.ArtifactID
		case "project.version", "pom.version":
			return p.Version()
		}
		if v, ok := p.Properties[name]; ok {
			return v
		}
		return ref
	})
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

func (p *PomXML) hasSCM() bool {
	return p.SCMURL != "" || p.SCMConnection != "" || p.SCMDeveloperConnection != ""
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
