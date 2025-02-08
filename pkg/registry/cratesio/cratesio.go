// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package cratesio provides interfaces for interacting with the crates.io API and with Cargo-specific formats.
package cratesio

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

var registryURL = urlx.MustParse("https://crates.io")

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
	Crate(context.Context, string) (*Crate, error)
	Version(context.Context, string, string) (*CrateVersion, error)
	Artifact(context.Context, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the crates.io HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Crate provides all API information related to the given crate.
func (r HTTPRegistry) Crate(ctx context.Context, pkg string) (*Crate, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/crates", pkg))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching crate metadata")
	}
	var c Crate
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	for i := range c.Versions {
		downloadPath, err := url.Parse(c.Versions[i].DownloadPath)
		if err != nil {
			return nil, errors.Wrap(err, "parsing version download path")
		}
		c.Versions[i].DownloadURL = registryURL.ResolveReference(downloadPath).String()
	}
	return &c, nil
}

// Version provides all API information related to the given version of a crate.
func (r HTTPRegistry) Version(ctx context.Context, pkg, version string) (*CrateVersion, error) {
	pathURL, err := url.Parse(path.Join("/api/v1/crates", pkg, version))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching version")
	}
	var v CrateVersion
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	downloadPath, err := url.Parse(v.DownloadPath)
	if err != nil {
		return nil, errors.Wrap(err, "parsing version download path")
	}
	v.DownloadURL = registryURL.ResolveReference(downloadPath).String()
	return &v, nil
}

// Artifact provides the crate artifact associated with a specific crate version.
func (r HTTPRegistry) Artifact(ctx context.Context, pkg string, version string) (io.ReadCloser, error) {
	vmeta, err := r.Version(ctx, pkg, version)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, vmeta.DownloadURL, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching artifact")
	}
	return resp.Body, nil
}

var _ Registry = &HTTPRegistry{}
