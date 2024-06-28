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

// Package pypi describes the PyPi registry interface.
package pypi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

var registryURL, _ = url.Parse("https://pypi.org")

// Project describes a single PyPi project with multiple releases.
type Project struct {
	Info     `json:"info"`
	Releases map[string][]Artifact `json:"releases"`
}

// Release describes a single PyPi project version with multiple artifacts.
type Release struct {
	Info      `json:"info"`
	Artifacts []Artifact `json:"urls"`
}

// Info about a project.
type Info struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Homepage    string            `json:"home_page"`
	ProjectURLs map[string]string `json:"project_urls"`
}

// An Artifact is one out of the multiple files that can be included in a release.
//
// PyPi might refer to this object as a "package" which is why it has a PackageType.
type Artifact struct {
	Digests       `json:"digests"`
	Filename      string    `json:"filename"`
	Size          int64     `json:"size"`
	PackageType   string    `json:"packagetype"`
	PythonVersion string    `json:"python_version"`
	URL           string    `json:"url"`
	UploadTime    time.Time `json:"upload_time_iso_8601"`
}

// Digests are the hashes of the artifact.
type Digests struct {
	MD5    string `json:"md5"`
	SHA256 string `json:"sha256"`
}

// Registry is an PyPI package registry.
type Registry interface {
	Project(context.Context, string) (*Project, error)
	Release(context.Context, string, string) (*Release, error)
	Artifact(context.Context, string, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the pypi.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Project provides all API information related to the given package.
func (r HTTPRegistry) Project(ctx context.Context, pkg string) (*Project, error) {
	pathURL, err := url.Parse(path.Join("/pypi", pkg, "json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("pypi registry error: %v", resp.Status)
	}
	var p Project
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Release provides all API information related to the given version of a package.
func (r HTTPRegistry) Release(ctx context.Context, pkg, version string) (*Release, error) {
	pathURL, err := url.Parse(path.Join("/pypi", pkg, version, "json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("pypi registry error: %v", resp.Status)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// Artifact provides the artifact associated with a specific package version.
func (r HTTPRegistry) Artifact(ctx context.Context, pkg, version, filename string) (io.ReadCloser, error) {
	release, err := r.Release(ctx, pkg, version)
	if err != nil {
		return nil, err
	}
	for _, artifact := range release.Artifacts {
		if artifact.Filename == filename {
			req, _ := http.NewRequest(http.MethodGet, artifact.URL, nil)
			resp, err := r.Client.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				return nil, errors.Errorf("fetching artifact: %v", resp.Status)
			}
			return resp.Body, nil
		}

	}
	return nil, errors.New("not found")
}

var _ Registry = &HTTPRegistry{}
