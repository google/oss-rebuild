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
// from an empty checkout of the upstream repo, written with gzip.NoCompression.
//
// # Fill Budgets
//
// A fill stops at a time and size budget. One over its size budget fails with
// 413.
//
// # Failed Fills
//
// A fill over its size budget, or the third failure in a row, makes refills
// fail fast for a day with 413 or 422.
//
// # Concurrent Fills
//
// A lease beside the entry lets one request at a time fill it. Others get 503
// with Retry-After.
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
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/gcsx"
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
	s := &Server{backend: backend, cloneFunc: gitx.Clone}
	switch total, err := MemTotal(); {
	case err != nil:
		log.Printf("no fill budget: %v", err)
	case total <= fillMemoryReserve:
		log.Printf("no fill budget: %d MiB is within the reserve", total>>20)
	default:
		s.fillBytes = total - fillMemoryReserve
	}
	return s, nil
}

// A fill stages its repository in memory, so it may use the memory beyond
// fillMemoryReserve. The budget covers a pack split's or repack's second
// copy, since watchSize counts it.
const fillMemoryReserve = 2 << 30

// MemTotal reads the total memory from /proc/meminfo. On Cloud Run, that is
// the container's memory limit.
func MemTotal() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(b)) {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10, errors.Wrap(err, "parsing MemTotal")
		}
	}
	return 0, errors.New("no MemTotal in /proc/meminfo")
}

// newBackend creates a cacheBackend from the given cache location string.
func newBackend(ctx context.Context, cacheStr string) (cacheBackend, error) {
	if strings.HasPrefix(cacheStr, "gs://") {
		bucketName, _, _ := gcsx.ParseURL(cacheStr)
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
