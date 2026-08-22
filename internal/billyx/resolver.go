// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/gcsx"
	"github.com/pkg/errors"
)

// Resolver maps a location to the local or GCS filesystem that owns it.
type Resolver struct {
	mu      sync.Mutex
	client  *storage.Client
	dialErr error
}

// NewResolver returns a Resolver with nothing dialed.
func NewResolver() *Resolver { return &Resolver{} }

// FS returns the filesystem owning uri and uri's path within it.
// For GCS, ctx governs GCS initialization and every subsequent operation on
// the filesystem. file:// URIs and bare paths are local while relative paths
// resolve against the working directory.
func (r *Resolver) FS(ctx context.Context, uri string) (billy.Filesystem, string, error) {
	if strings.HasPrefix(uri, "gs://") {
		bucket, object, err := gcsx.ParseURL(uri)
		if err != nil {
			return nil, "", err
		}
		r.mu.Lock()
		if r.client == nil && r.dialErr == nil {
			r.client, r.dialErr = storage.NewClient(ctx)
		}
		client, err := r.client, r.dialErr
		r.mu.Unlock()
		if err != nil {
			return nil, "", errors.Wrap(err, "creating storage client")
		}
		return NewGCS(ctx, client, bucket, ""), object, nil
	}
	path, err := filepath.Abs(strings.TrimPrefix(uri, "file://"))
	if err != nil {
		return nil, "", err
	}
	return osfs.New("/"), path, nil
}

// DirFS returns the filesystem rooted at uri, for callers addressing a
// destination rather than a file within one.
func (r *Resolver) DirFS(ctx context.Context, uri string) (billy.Filesystem, error) {
	fs, path, err := r.FS(ctx, uri)
	if err != nil {
		return nil, err
	}
	dir, err := fs.Chroot(path)
	return dir, errors.Wrapf(err, "resolving %s", uri)
}
