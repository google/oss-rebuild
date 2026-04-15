// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package gitcache implements a git repo cache on GCS or local filesystem.
//
// The served API is as follows:
//
//	/get: Serve the cached repo metadata object, populating the cache if necessary.
//	  - uri: Git repo URI e.g. github.com/org/repo
//	  - contains: The RFC3339-formatted time after which a cache entry must have been created.
//	  - ref: Git reference (branch/tag) to cache. If provided, creates a separate cache entry per ref.
//
// For GCS backend, redirects to GCS URL. For local backend, serves file directly.
//
// # Storage Backends
//
// The -cache flag accepts either:
//   - gs://bucket-name: Use GCS storage
//   - /path/to/dir or file:///path/to/dir: Use local filesystem storage
//
// # Object Format
//
// The repo cache is stored as a gzipped tar archive of the .git/ directory
// from an empty checkout of the upstream repo.
//
// # Data Races
//
// Racing requests for the same resource will write and return different copies
// of the repo but these are expected to be ~identical and, given the storage
// backend's write semantics, subsequent requests will converge to return the
// latest version of the archive.
//
// The current behavior could be improved by coalescing like requests and
// blocking on a single writer.
//
// # Cache Lifecycle
//
// If the caller provides the "contains" parameter that is more recent than the
// most recent cache entry, it will be re-fetched and overwritten.
//
// There is currently no TTL for cache entries nor a size limitation for the
// backing storage. These are areas for future work.
package gitcache

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/pkg/errors"
)

// GetRequest represents a request to the /get endpoint.
type GetRequest struct {
	URI      string `form:"uri"`
	Contains string `form:"contains"`
	Ref      string `form:"ref"`
}

// Validate checks that the request fields are valid.
func (r GetRequest) Validate() error {
	if r.URI == "" {
		return errors.New("Empty URI")
	}
	if r.Contains != "" {
		if _, err := time.Parse(time.RFC3339, r.Contains); err != nil {
			return errors.Wrap(err, "Failed to parse RFC 3339 time")
		}
	}
	return nil
}

// NewServer creates a new Server with the given cache location string.
func NewServer(ctx context.Context, cacheStr string) (*Server, error) {
	backend, err := newBackend(ctx, cacheStr)
	if err != nil {
		return nil, err
	}
	return &Server{backend: backend, cloneFunc: gitx.Clone}, nil
}

// newBackend creates a cacheBackend from the given cache location string.
func newBackend(ctx context.Context, cacheStr string) (cacheBackend, error) {
	if strings.HasPrefix(cacheStr, "gs://") {
		bucketName := strings.TrimPrefix(cacheStr, "gs://")
		bucketName = strings.TrimSuffix(bucketName, "/")
		client, err := storage.NewClient(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating GCS client")
		}
		if _, err := client.Bucket(bucketName).Attrs(ctx); err != nil {
			return nil, errors.Wrapf(err, "accessing bucket gs://%s", bucketName)
		}
		log.Printf("Using GCS backend: gs://%s", bucketName)
		return &gcsBackend{client: client, bucket: bucketName}, nil
	}
	cacheDir := cacheStr
	if strings.HasPrefix(cacheDir, "file://") {
		cacheDir = strings.TrimPrefix(cacheDir, "file://")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, errors.Wrap(err, "creating cache directory")
	}
	log.Printf("Using local backend: %s", cacheDir)
	return &localBackend{baseDir: cacheDir}, nil
}
