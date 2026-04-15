// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"log"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
)

// cacheBackend provides storage operations for both GCS and local filesystem.
type cacheBackend interface {
	// exists checks if the cache entry exists and returns its modification time.
	// Returns zero time and nil error if not found.
	exists(ctx context.Context, path string) (mtime time.Time, err error)
	// writer returns a WriteCloser for writing data to the cache entry.
	// The caller must call Close to finalize the write.
	writer(ctx context.Context, path string) (io.WriteCloser, error)
	// serve serves the cached content to the response writer.
	serve(rw http.ResponseWriter, req *http.Request, path string)
	// delete removes the cache entry.
	delete(ctx context.Context, path string) error
}

// gcsBackend implements cacheBackend for GCS storage.
type gcsBackend struct {
	client *storage.Client
	bucket string
}

func (g *gcsBackend) exists(ctx context.Context, path string) (time.Time, error) {
	o := g.client.Bucket(g.bucket).Object(path)
	a, err := o.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return a.Updated, nil
}

func (g *gcsBackend) writer(ctx context.Context, path string) (io.WriteCloser, error) {
	o := g.client.Bucket(g.bucket).Object(path)
	return o.NewWriter(ctx), nil
}

func (g *gcsBackend) serve(rw http.ResponseWriter, req *http.Request, path string) {
	o := g.client.Bucket(g.bucket).Object(path)
	a, err := o.Attrs(req.Context())
	if err != nil {
		log.Printf("Failed to get attrs for gs://%s/%s: %v", g.bucket, path, err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	rawPath := fmt.Sprintf("download/storage/v1/b/%s/o/%s", g.bucket, url.QueryEscape(path))
	redirect := url.URL{
		Scheme:   "https",
		Host:     "storage.googleapis.com",
		Path:     rawPath,
		RawPath:  rawPath,
		RawQuery: fmt.Sprintf("generation=%d&alt=media", a.Generation),
	}
	http.Redirect(rw, req, redirect.String(), http.StatusFound)
}

func (g *gcsBackend) delete(ctx context.Context, path string) error {
	o := g.client.Bucket(g.bucket).Object(path)
	err := o.Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

// localBackend implements cacheBackend for local filesystem storage.
type localBackend struct {
	baseDir string
}

func (l *localBackend) exists(ctx context.Context, path string) (time.Time, error) {
	fullPath := filepath.Join(l.baseDir, path)
	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// atomicFileWriter writes to a temp file and renames to the final path on Close.
type atomicFileWriter struct {
	finalPath string
	tmpFile   *os.File
}

func (w *atomicFileWriter) Write(p []byte) (int, error) {
	return w.tmpFile.Write(p)
}

func (w *atomicFileWriter) Close() error {
	tmpPath := w.tmpFile.Name()
	if err := w.tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return errors.Wrap(err, "closing temp file")
	}
	if err := os.Rename(tmpPath, w.finalPath); err != nil {
		os.Remove(tmpPath)
		return errors.Wrap(err, "renaming temp file to final path")
	}
	return nil
}

func (l *localBackend) writer(ctx context.Context, path string) (io.WriteCloser, error) {
	fullPath := filepath.Join(l.baseDir, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, errors.Wrap(err, "creating cache directory")
	}
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return nil, errors.Wrap(err, "creating temp file")
	}
	return &atomicFileWriter{finalPath: fullPath, tmpFile: tmpFile}, nil
}

func (l *localBackend) serve(rw http.ResponseWriter, req *http.Request, path string) {
	fullPath := filepath.Join(l.baseDir, path)
	http.ServeFile(rw, req, fullPath)
}

func (l *localBackend) delete(ctx context.Context, path string) error {
	fullPath := filepath.Join(l.baseDir, path)
	err := os.Remove(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
