// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package maven provides an interface with Maven package registry and its API.
package maven

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var (
	// Maven Central registry.
	registryURL = urlx.MustParse("https://search.maven.org")
	releaseURL  = urlx.MustParse("https://repo1.maven.org/maven2")
	// Maven path component.
	registryContentPathURL = urlx.MustParse("remotecontent")
	registrySearchPathURL  = urlx.MustParse(path.Join("solrsearch", "select"))
)

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
	ReleaseURL(context.Context, string, string, string) (string, error)
	Artifact(context.Context, string, string, string) (io.ReadCloser, error)
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
		return nil, errors.New("package identifier not of form 'group:artifact'")
	}

	pathUrl := registryURL.ResolveReference(registrySearchPathURL)
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
		return nil, errors.Wrap(errors.New(resp.Status), "fetching package version")
	}
	defer resp.Body.Close()
	var s search
	err = json.NewDecoder(resp.Body).Decode(&s)
	results := s.Response.Docs
	if err != nil {
		return nil, err
	} else if len(results) == 0 {
		return nil, errors.New("not found")
	} else if len(results) > 1 {
		return nil, errors.New("multiple matches found")
	} else {
		result = &results[0]
	}
	result.Published = time.UnixMilli(result.PublishedMilli)
	return result, nil
}

// PackageMetadata returns the metadata for a Maven package.
func (r HTTPRegistry) PackageMetadata(ctx context.Context, pkg string) (result *MavenPackage, err error) {
	content, err := r.ReleaseFile(ctx, pkg, "maven", TypeMetadata)
	if err != nil {
		return nil, err
	}
	defer content.Close()
	err = xml.NewDecoder(content).Decode(&result)
	if err != nil {
		return nil, err
	}
	result.LastUpdated, err = time.Parse("20060102150405", result.LastUpdatedString)
	return result, nil
}

// ReleaseFile returns a release file for a Maven package version.
func (r HTTPRegistry) ReleaseFile(ctx context.Context, pkg, version string, typ string) (io.ReadCloser, error) {
	releaseURL, err := r.ReleaseURL(ctx, pkg, version, typ)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching release artifact")
	}

	return resp.Body, nil
}

// ReleaseURL returns the URL for the release file for a Maven package version.
func (r HTTPRegistry) ReleaseURL(ctx context.Context, pkg, version, typ string) (string, error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		return "", errors.New("package identifier not of form 'group:artifact'")
	}
	artifactPath := path.Join(strings.ReplaceAll(g, ".", "/"), a, version, fmt.Sprintf("%s-%s%s", a, version, typ))
	artifactURL := releaseURL.JoinPath(artifactPath)
	return artifactURL.String(), nil
}

// Artifact returns file that is part of the Maven release.
func (r HTTPRegistry) Artifact(ctx context.Context, pkg, version, artifact string) (io.ReadCloser, error) {
	g, a, found := strings.Cut(pkg, ":")
	if !found {
		return nil, errors.New("package identifier not of form 'group:artifact'")
	}
	artifactPath := path.Join(strings.ReplaceAll(g, ".", "/"), a, version, artifact)
	artifactURL := releaseURL.JoinPath(artifactPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "creating artifact URL")
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "fetching artifact")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching artifact")
	}
	return resp.Body, nil
}
