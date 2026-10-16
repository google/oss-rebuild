// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"io"
	"net/url"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/dump"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// CrateRequest asks for a crate's metadata, as /api/v1/crates/<name> serves it.
type CrateRequest struct {
	Name string `form:"name"`
}

func (r CrateRequest) Validate() error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

// VersionRequest asks for a crate version's metadata, as
// /api/v1/crates/<name>/<version> serves it.
type VersionRequest struct {
	Name    string `form:"name"`
	Version string `form:"version"`
}

func (r VersionRequest) Validate() error {
	if r.Name == "" || r.Version == "" {
		return errors.New("name and version are required")
	}
	return nil
}

// MetadataDeps holds the crates.io dump, nil while none is loaded.
type MetadataDeps struct {
	Dump *dump.Index
}

var errNotLoaded = api.AsStatus(codes.Unavailable, errors.New("crates.io dump not loaded"))

// Crate serves a crate's metadata from the dump.
func Crate(_ context.Context, req CrateRequest, deps *MetadataDeps) (*cratesio.Crate, error) {
	if deps.Dump == nil {
		return nil, errNotLoaded
	}
	c, ok := deps.Dump.Crate(req.Name)
	if !ok {
		return nil, api.AsStatus(codes.NotFound, errors.Errorf("crate %s not in the dump", req.Name))
	}
	return c, nil
}

// Version serves a crate version's metadata from the dump.
func Version(_ context.Context, req VersionRequest, deps *MetadataDeps) (*cratesio.CrateVersion, error) {
	if deps.Dump == nil {
		return nil, errNotLoaded
	}
	v, ok := deps.Dump.Version(req.Name, req.Version)
	if !ok {
		return nil, api.AsStatus(codes.NotFound, errors.Errorf("%s@%s not in the dump", req.Name, req.Version))
	}
	return v, nil
}

// Cache is the crates registry service's daily copy of crates.io's crate and
// version records.
type Cache struct {
	crate   api.StubFn[CrateRequest, cratesio.Crate]
	version api.StubFn[VersionRequest, cratesio.CrateVersion]
}

// NewCache calls the crates registry service at u.
func NewCache(c httpx.BasicClient, u *url.URL) *Cache {
	return &Cache{
		crate:   api.Stub[CrateRequest, cratesio.Crate](c, u.JoinPath("crate")),
		version: api.Stub[VersionRequest, cratesio.CrateVersion](c, u.JoinPath("version")),
	}
}

// BackedBy returns a crates.io registry that reads the cache, then live on any
// error and for every artifact. A cached crate lists versions as of the dump.
func (c *Cache) BackedBy(live cratesio.Registry) cratesio.Registry {
	return backedCache{cache: c, live: live}
}

type backedCache struct {
	cache *Cache
	live  cratesio.Registry
}

func (r backedCache) Crate(ctx context.Context, name string) (*cratesio.Crate, error) {
	if c, err := r.cache.crate(ctx, CrateRequest{Name: name}); err == nil {
		return c, nil
	}
	return r.live.Crate(ctx, name)
}

func (r backedCache) Version(ctx context.Context, name, version string) (*cratesio.CrateVersion, error) {
	if v, err := r.cache.version(ctx, VersionRequest{Name: name, Version: version}); err == nil {
		return v, nil
	}
	return r.live.Version(ctx, name, version)
}

func (r backedCache) Artifact(ctx context.Context, name, version string) (io.ReadCloser, error) {
	return r.live.Artifact(ctx, name, version)
}
