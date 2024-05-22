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

// Package maven provides an interface with Maven package registry and its API.
package maven

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// PomXML is the root element of a Maven POM file.
type PomXML struct {
	GroupID    string `xml:"project>groupId"`
	ArtifactID string `xml:"project>artifactId"`
	VersionID  string `xml:"project>version"`
	URL        string `xml:"project>url"`
	SCMURL     string `xml:"project>scm>url"`
	Parent     Parent `xml:"project>parent"`
}

// Parent represents the parent package ref within a Maven POM file.
type Parent struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	VersionID  string `xml:"version"`
}

// Repo returns the repository URL for a Maven package.
func (p PomXML) Repo() string {
	if p.SCMURL != "" {
		return p.SCMURL
	}
	return p.URL
}

// Name returns the Maven package name.
func (p PomXML) Name() string {
	return p.Group() + ":" + p.ArtifactID
}

// Group returns the Maven package group.
func (p PomXML) Group() string {
	if g := p.GroupID; g != "" {
		return g
	}
	return p.Parent.GroupID
}

// Version returns the Maven package version.
func (p PomXML) Version() string {
	if v := p.VersionID; v != "" {
		return v
	}
	return p.Parent.VersionID
}

// MavenPackage is a Maven package.
type MavenPackage struct {
	MavenMetadata `xml:"metadata"`
}

// MavenMetadata is the root element of a Maven metadata file.
type MavenMetadata struct {
	GroupID           string   `xml:"groupId"`
	ArtifactID        string   `xml:"artifactId"`
	Versions          []string `xml:"versioning>versions>version"`
	LastUpdatedString string   `xml:"versioning>lastUpdated"`
	LastUpdated       time.Time
}

// PackageMetadata returns the metadata for a Maven package.
func PackageMetadata(pkg string) (result MavenPackage, err error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		err = errors.New("package identifier not of form 'group:artifact'")
		return
	}
	path := filepath.Join(strings.ReplaceAll(g, ".", "/"), a, "maven-metadata.xml")
	resp, err := http.Get(fmt.Sprintf("https://search.maven.org/remotecontent?filepath=%s", path))
	if err != nil {
		return
	}
	if resp.StatusCode != http.StatusOK {
		err = errors.Errorf("maven registry error: %s", resp.Status)
		return
	}
	err = xml.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return
	}
	result.LastUpdated, err = time.Parse("20060102150405", result.LastUpdatedString)
	return
}

// search is a Maven JSON search API response.
type search struct {
	Response response `json:"response"`
}

type response struct {
	Docs []MavenVersion `json:"docs"`
}

// MavenVersion is the metadata for a Maven package version.
type MavenVersion struct {
	GroupID        string `json:"g"`
	ArtifactID     string `json:"a"`
	Version        string `json:"v"`
	PublishedMilli int64  `json:"timestamp"`
	Published      time.Time
	Files          []FileType `json:"ec"`
}

// FileType is a Maven package file type.
type FileType string

const (
	// TypePOM is a POM file.
	TypePOM FileType = ".pom"
	// TypeSources is a sources file.
	TypeSources FileType = "-sources.jar"
	// TypeJar is a jar file.
	TypeJar FileType = ".jar"
	// TypeJavadoc is a javadoc file.
	TypeJavadoc FileType = "-javadoc.jar"
	// TypeModule is a module file.
	TypeModule FileType = ".module"
)

// VersionMetadata returns the metadata for a Maven package version.
func VersionMetadata(pkg, version string) (result MavenVersion, err error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		err = errors.New("package identifier not of form 'group:artifact'")
		return
	}
	resp, err := http.Get(fmt.Sprintf("https://search.maven.org/solrsearch/select?rows=5&wt=json&core=gav&q=g:%s+AND+a:%s+AND+v:%s", g, a, version))
	if err != nil {
		return
	}
	if resp.StatusCode != http.StatusOK {
		err = errors.Errorf("maven registry error: %s", resp.Status)
		return
	}
	var s search
	err = json.NewDecoder(resp.Body).Decode(&s)
	results := s.Response.Docs
	if err != nil {
		return
	} else if results == nil || len(results) == 0 {
		err = errors.New("not found")
	} else if len(results) > 1 {
		err = errors.New("multiple matches found")
	} else {
		result = results[0]
	}
	result.Published = time.UnixMilli(result.PublishedMilli)
	return
}

// ReleaseFile returns a release file for a Maven package version.
func ReleaseFile(pkg, version string, typ FileType) (r io.ReadCloser, err error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		err = errors.New("package identifier not of form 'group:artifact'")
		return
	}
	path := filepath.Join(strings.ReplaceAll(g, ".", "/"), a, version, fmt.Sprintf("%s-%s%s", a, version, typ))
	resp, err := http.Get("https://search.maven.org/remotecontent?filepath=" + path)
	if err != nil {
		return
	}
	if resp.StatusCode != http.StatusOK {
		err = errors.Errorf("maven registry error: %s", resp.Status)
		return
	}
	r = resp.Body
	return
}

// VersionPomXML returns the POM file for a Maven package version.
func VersionPomXML(pkg, version string) (p PomXML, err error) {
	var r io.ReadCloser
	r, err = ReleaseFile(pkg, version, TypePOM)
	if err != nil {
		return
	}
	defer r.Close()
	err = xml.NewDecoder(r).Decode(&p)
	return
}
