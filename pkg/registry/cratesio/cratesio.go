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

// Package cratesio provides interfaces for interacting with the crates.io API and with Cargo-specific formats.
package cratesio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/pkg/errors"
)

var registryURL, _ = url.Parse("https://crates.io")

// Crate is the /api/v1/crates/<name> result.
type Crate struct {
	Metadata `json:"crate"`
	Versions []Version `json:"versions"`
}

// Metadata is the crate-specific information returned by the API.
type Metadata struct {
	Name       string    `json:"id"`
	Repository string    `json:"repository"`
	Created    time.Time `json:"created_at"`
	Updated    time.Time `json:"updated_at"`
}

// Version is the create-version-specific metadata returned by the API.
type Version struct {
	Version      string    `json:"num"`
	RustVersion  string    `json:"rust_version"`
	DownloadPath string    `json:"dl_path"`
	Created      time.Time `json:"created_at"`
	Updated      time.Time `json:"updated_at"`
	Yanked       bool      `json:"yanked"`
	DownloadURL  string
}

// CrateVersion is the /api/v1/crates/<name>/<version> result.
type CrateVersion struct {
	Version `json:"version"`
}

// Registry is a crates.io package registry.
type Registry interface {
	Crate(string) (*Crate, error)
	Version(string, string) (*CrateVersion, error)
	Artifact(string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the crates.io HTTP API.
type HTTPRegistry struct {
	Client httpinternal.BasicClient
}

// Crate provides all API information related to the given crate.
func (r HTTPRegistry) Crate(pkg string) (*Crate, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/crates", pkg))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("crates.io registry error: %s", resp.Status)
	}
	var c Crate
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	for i := range c.Versions {
		downloadPath, _ := url.Parse(c.Versions[i].DownloadPath)
		c.Versions[i].DownloadURL = registryURL.ResolveReference(downloadPath).String()
	}
	return &c, nil
}

// Version provides all API information related to the given version of a crate.
func (r HTTPRegistry) Version(pkg, version string) (*CrateVersion, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/crates", pkg, version))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("crates.io registry error: %s", resp.Status)
	}
	var v CrateVersion
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	downloadPath, _ := url.Parse(v.DownloadPath)
	v.DownloadURL = registryURL.ResolveReference(downloadPath).String()
	return &v, nil
}

// Artifact provides the crate artifact associated with a specific crate version.
func (r HTTPRegistry) Artifact(pkg string, version string) (io.ReadCloser, error) {
	vmeta, err := r.Version(pkg, version)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, vmeta.DownloadURL, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("fetching artifact: %s", resp.Status)
	}
	return resp.Body, nil
}

var _ Registry = &HTTPRegistry{}
