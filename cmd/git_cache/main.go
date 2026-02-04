// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package main implements a git repo cache on GCS or local filesystem.
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
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/pkg/errors"
)

var (
	cache = flag.String("cache", "", "cache location: gs://bucket-name for GCS, or /path/to/dir for local filesystem")
	port  = flag.Int("port", 8080, "port on which to serve")
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

var backend cacheBackend

var thresholdFudgeFactor = 24 * time.Hour

type getRequest struct {
	URI       string
	Threshold time.Time
	Ref       string
}

func parseGetRequest(v url.Values) (r getRequest, err error) {
	r.URI = v.Get("uri")
	if r.URI == "" {
		return r, errors.New("Empty URI")
	}
	qthreshold := v.Get("contains")
	if qthreshold != "" {
		r.Threshold, err = time.Parse(time.RFC3339, qthreshold)
		if err != nil {
			return r, errors.Wrap(err, "Failed to parse RFC 3339 time")
		}
	}
	r.Ref = v.Get("ref")
	return r, nil
}

func HandleGet(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	if err := req.ParseForm(); err != nil {
		log.Printf("Failed to parse form data: %v", err)
		http.Error(rw, "Bad form data", 400)
		return
	}
	r, err := parseGetRequest(req.Form)
	if err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	if r.Threshold.After(time.Now().Add(thresholdFudgeFactor)) {
		http.Error(rw, "Time bound too far in the future", 400)
		return
	}
	u, err := uri.CanonicalizeRepoURI(r.URI)
	if err != nil {
		log.Printf("Failed to canonicalize %s: %v\n", r.URI, err)
		http.Error(rw, "Failed to canonicalize repo URI", 400)
		return
	}
	u = strings.TrimPrefix(u, "https://")
	// Expect <host>/<org>/<repo>.
	if parts := strings.Split(u, "/"); len(parts) != 3 {
		http.Error(rw, "Unsupported repo URI", 400)
		return
	}
	// Normalize repo URI to provide the following interface:
	// {cache}/<host>/<org>/<repo>/repo.tgz (default branch)
	// {cache}/<host>/<org>/<repo>/<ref>/repo.tgz (specific ref)
	var p string
	if r.Ref != "" {
		// Include ref in path to create separate cache entries per ref
		refPath := strings.ReplaceAll(r.Ref, "/", "_") // Replace slashes to avoid path issues
		p = filepath.Join(strings.ToLower(u), refPath, "repo.tgz")
	} else {
		p = filepath.Join(strings.ToLower(u), "repo.tgz")
	}
	mtime, err := backend.exists(ctx, p)
	needsPopulate := false
	if err != nil {
		log.Printf("Error checking cache for %s: %v\n", p, err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	if mtime.IsZero() {
		needsPopulate = true
	} else if mtime.Before(r.Threshold) {
		log.Printf("Refreshing cache for %s: entry fetched %s does not contain requested %s\n", p, mtime.Format(time.RFC3339), r.Threshold.Format(time.RFC3339))
		needsPopulate = true
	}
	if needsPopulate {
		if err := populateCache(ctx, u, r.Ref, p); err != nil {
			log.Printf("Failed to populate cache: %v\n", err)
			if errors.Is(err, transport.ErrAuthenticationRequired) {
				http.Error(rw, err.Error(), 400)
			} else {
				if err := backend.delete(ctx, p); err != nil {
					log.Printf("Issue cleaning up failed write: %v\n", err)
				}
				http.Error(rw, "Internal Error", 500)
			}
			return
		}
	}
	backend.serve(rw, req, p)
}

// nilCache is a no-op cache for git objects.
type nilCache struct{}

func (c nilCache) Get(plumbing.Hash) (plumbing.EncodedObject, bool) { return nil, false }
func (c nilCache) Put(plumbing.EncodedObject)                       {}
func (c nilCache) Clear()                                           {}

// populateCache writes an archived bare checkout of the repo to the cache backend.
// NOTE: cachePath may have incomplete content even when err != nil.
func populateCache(ctx context.Context, repo, ref string, cachePath string) error {
	// Create a temp directory for cloning
	tmpDir, err := os.MkdirTemp("", "git-cache-*")
	if err != nil {
		return errors.Wrap(err, "creating temp directory")
	}
	defer os.RemoveAll(tmpDir)
	// Set up filesystem storage in the temp directory
	m := osfs.New(tmpDir)
	s := filesystem.NewStorage(m, nilCache{})
	cloneOpts := &git.CloneOptions{URL: "https://" + repo, NoCheckout: true}
	if ref != "" {
		cloneOpts.ReferenceName = plumbing.ReferenceName(ref)
		cloneOpts.SingleBranch = true
	}
	log.Printf("Cloning %s", repo)
	// gitx.Clone will use native git if available, otherwise fall back to go-git
	if _, err := gitx.Clone(ctx, s, nil, cloneOpts); err != nil {
		if ref != "" {
			return errors.Wrapf(err, "failure cloning %s at ref %s", repo, ref)
		}
		return errors.Wrapf(err, "failure cloning %s", repo)
	}
	log.Println("Clone successful")
	w, err := backend.writer(ctx, cachePath)
	if err != nil {
		return errors.Wrapf(err, "failure opening cache writer for %s", cachePath)
	}
	if err := createTarball(tmpDir, w); err != nil {
		// Close but ignore error; the tarball write already failed.
		// For localBackend this still renames the partial file, but the
		// caller will delete the cache entry on error anyway.
		w.Close()
		return errors.Wrapf(err, "failure archiving files in %s", repo)
	}
	if err := w.Close(); err != nil {
		return errors.Wrapf(err, "failure finalizing cache write for %s", cachePath)
	}
	return nil
}

// createTarball creates a gzipped tar archive of the given directory.
func createTarball(srcDir string, w io.Writer) error {
	m := osfs.New(srcDir)
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	var bytesWritten int64
	err := util.Walk(m, "/", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			// Fail on any path handling issue (see filepath.WalkFunc docs).
			return err
		}
		if path == string(filepath.Separator) {
			// Omit root directory.
			return nil
		}
		h, err := tar.FileInfoHeader(info, path)
		if err != nil {
			return err
		}
		// h.Name is initialized to info.Name() which only contains the base
		// filename. To fix this, we prepend the remainder of the path.
		h.Name = filepath.Join(filepath.Dir(path), h.Name)
		// TAR contents should be relative so make the path relative to the root.
		h.Name, _ = filepath.Rel(string(filepath.Separator), h.Name)
		if info.IsDir() {
			// The .git/refs dir defaults to 0o666 which breaks the directory for the
			// extracting user. To fix this, use permissive access for all dirs.
			h.Mode = h.Mode | int64(fs.ModePerm)
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			f, err := m.Open(path)
			if err != nil {
				return err
			}
			if written, err := io.Copy(tw, f); err != nil {
				return err
			} else {
				bytesWritten += written
			}
			// Periodically flush to limit memory usage.
			if bytesWritten > 1_000_000 {
				bytesWritten = 0
				if err := gw.Flush(); err != nil {
					return err
				}
			}
			// Remove completed file from filesystem.
			if err := m.Remove(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return errors.Wrap(err, "closing tar writer")
	}
	if err := gw.Close(); err != nil {
		return errors.Wrap(err, "closing gzip writer")
	}
	return nil
}

func main() {
	flag.Parse()
	if *cache == "" {
		log.Fatalln("-cache flag is required")
	}
	// Parse cache location to determine backend type
	if strings.HasPrefix(*cache, "gs://") {
		// GCS backend
		bucketName := strings.TrimPrefix(*cache, "gs://")
		bucketName = strings.TrimSuffix(bucketName, "/")
		ctx := context.Background()
		client, err := storage.NewClient(ctx)
		if err != nil {
			log.Fatalf("Failed to create GCS client: %v", err)
		}
		if _, err := client.Bucket(bucketName).Attrs(ctx); err != nil {
			log.Fatalf("Failed to access bucket gs://%s: %v", bucketName, err)
		}
		backend = &gcsBackend{client: client, bucket: bucketName}
		log.Printf("Using GCS backend: gs://%s", bucketName)
	} else {
		// Local filesystem backend
		cacheDir := *cache
		// Handle file:// prefix
		if strings.HasPrefix(cacheDir, "file://") {
			cacheDir = strings.TrimPrefix(cacheDir, "file://")
		}
		// Ensure the directory exists
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			log.Fatalf("Failed to create cache directory: %v", err)
		}
		backend = &localBackend{baseDir: cacheDir}
		log.Printf("Using local backend: %s", cacheDir)
	}
	http.HandleFunc("/get", HandleGet)
	log.Printf("Listening on port %d", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalln(err)
	}
}
