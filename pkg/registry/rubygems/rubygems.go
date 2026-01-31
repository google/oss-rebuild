// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rubygems describes the RubyGems registry interface.
package rubygems

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var registryURL = urlx.MustParse("https://rubygems.org")

// Gem describes a single RubyGem's metadata.
type Gem struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Authors       string   `json:"authors"`
	Description   string   `json:"description"`
	Homepage      string   `json:"homepage_uri"`
	SourceCode    string   `json:"source_code_uri"`
	BugTracker    string   `json:"bug_tracker_uri"`
	Changelog     string   `json:"changelog_uri"`
	Documentation string   `json:"documentation_uri"`
	GemURI        string   `json:"gem_uri"`
	ProjectURI    string   `json:"project_uri"`
	SHA           string   `json:"sha"`
	Platform      string   `json:"platform"`
	Licenses      []string `json:"licenses"`
}

// VersionInfo describes a single version of a gem.
type VersionInfo struct {
	Number     string          `json:"number"`
	Platform   string          `json:"platform"`
	Prerelease bool            `json:"prerelease"`
	CreatedAt  time.Time       `json:"created_at"`
	SHA        string          `json:"sha"`
	Licenses   []string        `json:"licenses"`
	Metadata   VersionMetadata `json:"metadata"`
}

// VersionMetadata contains additional metadata for a version.
type VersionMetadata struct {
	SourceCodeURI string `json:"source_code_uri"`
	ChangelogURI  string `json:"changelog_uri"`
	HomepageURI   string `json:"homepage_uri"`
}

// Registry is a RubyGems package registry.
type Registry interface {
	Gem(context.Context, string) (*Gem, error)
	Versions(context.Context, string) ([]VersionInfo, error)
	Artifact(context.Context, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the rubygems.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Gem provides all API information related to the given gem.
func (r HTTPRegistry) Gem(ctx context.Context, name string) (*Gem, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/gems", name+".json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching gem")
	}
	var g Gem
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return nil, err
	}
	return &g, nil
}

// Versions provides all version information for the given gem.
func (r HTTPRegistry) Versions(ctx context.Context, name string) ([]VersionInfo, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/versions", name+".json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching versions")
	}
	var versions []VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// Artifact provides the gem file for a specific version.
func (r HTTPRegistry) Artifact(ctx context.Context, name, version string) (io.ReadCloser, error) {
	artifactURL := fmt.Sprintf("%s/gems/%s-%s.gem", registryURL.String(), name, version)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, errors.Wrap(errors.New(resp.Status), "fetching artifact")
	}
	return resp.Body, nil
}

// ArtifactURL returns the URL for downloading a gem artifact.
func (r HTTPRegistry) ArtifactURL(name, version string) string {
	return fmt.Sprintf("%s/gems/%s-%s.gem", registryURL.String(), name, version)
}

var _ Registry = &HTTPRegistry{}
