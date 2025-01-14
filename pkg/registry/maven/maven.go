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
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

var registryURL, _ = url.Parse("https://search.maven.org")

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
	Files          []string `json:"ec"`
}

const (
	// TypePOM is a POM file.
	TypePOM string = ".pom"
	// TypeSources is a sources file.
	TypeSources string = "-sources.jar"
	// TypeJar is a jar file.
	TypeJar string = ".jar"
	// TypeJavadoc is a javadoc file.
	TypeJavadoc string = "-javadoc.jar"
	// TypeModule is a module file.
	TypeModule   string = ".module"
	TypeMetadata string = "-metadata.xml"
)

// Registry is a Maven Central package registry.
type Registry interface {
	PackageMetadata(context.Context, string) (*MavenPackage, error)
	PackageVersion(context.Context, string, string) (*MavenVersion, error)
	ReleaseFile(context.Context, string, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the search.maven.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

var _ Registry = &HTTPRegistry{}

// VersionMetadata returns the metadata for a Maven package version.
func (r HTTPRegistry) PackageVersion(ctx context.Context, pkg, version string) (result *MavenVersion, err error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		err = errors.New("package identifier not of form 'group:artifact'")
		return
	}
	pathUrl, _ := url.Parse(path.Join("solrsearch", "select"))
	pathUrl = registryURL.ResolveReference(pathUrl)
	params := pathUrl.Query()
	params.Add("rows", "5")
	params.Add("wt", "json")
	params.Add("core", "gav")
	params.Add("q", fmt.Sprintf("g:%s+AND+a:%s+AND+v:%s", g, a, version))
	pathUrl.RawQuery = params.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pathUrl.String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("maven registry error: %v", resp.Status)
	}
	defer resp.Body.Close()
	var s search
	err = json.NewDecoder(resp.Body).Decode(&s)
	results := s.Response.Docs
	if err != nil {
		return
	} else if len(results) == 0 {
		err = errors.New("not found")
	} else if len(results) > 1 {
		err = errors.New("multiple matches found")
	} else {
		result = &results[0]
	}
	result.Published = time.UnixMilli(result.PublishedMilli)
	return
}

// PackageMetadata returns the metadata for a Maven package.
func (r HTTPRegistry) PackageMetadata(ctx context.Context, pkg string) (result *MavenPackage, err error) {
	content, err := r.ReleaseFile(ctx, pkg, "maven", TypeMetadata)
	if err != nil {
		return
	}
	defer content.Close()
	err = xml.NewDecoder(content).Decode(&result)
	if err != nil {
		return
	}
	result.LastUpdated, err = time.Parse("20060102150405", result.LastUpdatedString)
	return
}

// ReleaseFile returns a release file for a Maven package version.
func (r HTTPRegistry) ReleaseFile(ctx context.Context, pkg, version string, typ string) (io.ReadCloser, error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		return nil, errors.New("package identifier not of form 'group:artifact'")
	}
	path := filepath.Join(strings.ReplaceAll(g, ".", "/"), a, version, fmt.Sprintf("%s-%s%s", a, version, typ))
	pathUrl, _ := url.Parse("remotecontent")
	pathUrl = registryURL.ResolveReference(pathUrl)
	params := pathUrl.Query()
	params.Set("filepath", path)
	pathUrl.RawQuery = params.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pathUrl.String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("maven registry error: %v", resp.Status)
	}

	return resp.Body, nil
}
