// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

type NPMPackage struct {
	Name        string `json:"name"`
	DistTags    `json:"dist-tags"`
	Versions    map[string]Release   `json:"versions"`
	UploadTimes map[string]time.Time `json:"time"`
}
type DistTags struct {
	Latest string `json:"latest"`
}
type Release struct {
	Version       string `json:"version"`
	GitHEAD       string `json:"gitHead"`
	NPMVersion    string `json:"_npmVersion"`
	NodeVersion   string `json:"_nodeVersion"`
	Dist          `json:"dist"`
	RawRepository json.RawMessage `json:"repository"`
	Repository
	Scripts map[string]string `json:"scripts"`
}
type Repository struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Directory string `json:"directory"`
}
type Dist struct {
	URL    string `json:"tarball"`
	SHA1   string `json:"shasum"`
	SHA512 string `json:"integrity"`
}

type NPMVersion struct {
	Name          string `json:"name"`
	DistTags      `json:"dist-tags"`
	Version       string `json:"version"`
	GitHEAD       string `json:"gitHead"`
	NPMVersion    string `json:"_npmVersion"`
	NodeVersion   string `json:"_nodeVersion"`
	Dist          `json:"dist"`
	RawRepository json.RawMessage `json:"repository"`
	Repository
	Scripts map[string]string `json:"scripts"`
}

type PackageJSON struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Scripts map[string]string `json:"scripts"`
}

var registryURL = urlx.MustParse("https://registry.npmjs.org")

// Registry is an npm package registry.
type Registry interface {
	Package(context.Context, string) (*NPMPackage, error)
	Version(context.Context, string, string) (*NPMVersion, error)
	Artifact(context.Context, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the npmjs.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Package returns the package metadata for the given package.
func (r HTTPRegistry) Package(ctx context.Context, pkg string) (*NPMPackage, error) {
	pathURL, err := url.Parse(path.Join("/", pkg))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("npm registry error: %v", resp.Status)
	}
	var p NPMPackage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	for s, v := range p.Versions {
		if len(v.RawRepository) > 0 {
			if err := json.Unmarshal(v.RawRepository, &v.Repository); err != nil {
				// Try to parse out legacy unstructured URL format.
				if err := json.Unmarshal(v.RawRepository, &v.Repository.URL); err != nil {
					return nil, err
				}
			}
		}
		v.RawRepository = nil
		p.Versions[s] = v
	}
	return &p, nil
}

// Version returns the package metadata for the given package version.
func (r HTTPRegistry) Version(ctx context.Context, pkg, version string) (*NPMVersion, error) {
	pathURL, err := url.Parse(path.Join("/", pkg, version))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("npm registry error: %v", resp.Status)
	}
	var v NPMVersion
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	if len(v.RawRepository) > 0 {
		if err := json.Unmarshal(v.RawRepository, &v.Repository); err != nil {
			// Try to parse out legacy unstructured URL format.
			if err := json.Unmarshal(v.RawRepository, &v.Repository.URL); err != nil {
				return nil, err
			}
		}
	}
	v.RawRepository = nil
	return &v, nil
}

// Artifact returns the package artifact for the given package version.
func (r HTTPRegistry) Artifact(ctx context.Context, pkg, version string) (io.ReadCloser, error) {
	v, err := r.Version(ctx, pkg, version)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.Dist.URL, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("fetching artifact: %v", resp.Status)
	}
	return resp.Body, nil
}

var _ Registry = &HTTPRegistry{}
