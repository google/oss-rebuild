// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/pkg/errors"
)

var thresholdFudgeFactor = 24 * time.Hour

// Server provides HTTP handling for the git cache.
type Server struct {
	backend   cacheBackend
	cloneFunc gitx.CloneFunc
}

// HandleGet serves cached repo metadata, populating the cache if necessary.
func (s *Server) HandleGet(rw http.ResponseWriter, httpReq *http.Request) {
	ctx := httpReq.Context()
	if err := httpReq.ParseForm(); err != nil {
		log.Printf("Failed to parse form data: %v", err)
		http.Error(rw, "Bad form data", 400)
		return
	}
	var req GetRequest
	if err := form.Unmarshal(httpReq.Form, &req); err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	var threshold time.Time
	if req.Contains != "" {
		threshold, _ = time.Parse(time.RFC3339, req.Contains) // Already validated
	}
	if threshold.After(time.Now().Add(thresholdFudgeFactor)) {
		http.Error(rw, "Time bound too far in the future", 400)
		return
	}
	u, err := uri.CanonicalizeRepoURI(req.URI)
	if err != nil {
		log.Printf("Failed to canonicalize %s: %v\n", req.URI, err)
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
	if req.Ref != "" {
		// Include ref in path to create separate cache entries per ref
		refPath := strings.ReplaceAll(req.Ref, "/", "_") // Replace slashes to avoid path issues
		p = filepath.Join(strings.ToLower(u), refPath, "repo.tgz")
	} else {
		p = filepath.Join(strings.ToLower(u), "repo.tgz")
	}
	mtime, err := s.backend.exists(ctx, p)
	needsPopulate := false
	if err != nil {
		log.Printf("Error checking cache for %s: %v\n", p, err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	if mtime.IsZero() {
		needsPopulate = true
	} else if mtime.Before(threshold) {
		log.Printf("Refreshing cache for %s: entry fetched %s does not contain requested %s\n", p, mtime.Format(time.RFC3339), threshold.Format(time.RFC3339))
		needsPopulate = true
	}
	if needsPopulate {
		if err := s.populateCache(ctx, u, req.Ref, p); err != nil {
			log.Printf("Failed to populate cache: %v\n", err)
			if errors.Is(err, transport.ErrAuthenticationRequired) {
				http.Error(rw, err.Error(), 400)
			} else {
				if err := s.backend.delete(ctx, p); err != nil {
					log.Printf("Issue cleaning up failed write: %v\n", err)
				}
				http.Error(rw, "Internal Error", 500)
			}
			return
		}
	}
	s.backend.serve(rw, httpReq, p)
}

// nilCache is a no-op cache for git objects.
type nilCache struct{}

func (c nilCache) Get(plumbing.Hash) (plumbing.EncodedObject, bool) { return nil, false }
func (c nilCache) Put(plumbing.EncodedObject)                       {}
func (c nilCache) Clear()                                           {}

// populateCache writes an archived bare checkout of the repo to the cache backend.
// NOTE: cachePath may have incomplete content even when err != nil.
func (s *Server) populateCache(ctx context.Context, repo, ref string, cachePath string) error {
	// Create a temp directory for cloning
	tmpDir, err := os.MkdirTemp("", "git-cache-*")
	if err != nil {
		return errors.Wrap(err, "creating temp directory")
	}
	defer os.RemoveAll(tmpDir)
	// Set up filesystem storage in the temp directory
	m := osfs.New(tmpDir)
	st := filesystem.NewStorage(m, nilCache{})
	cloneOpts := &git.CloneOptions{URL: "https://" + repo, NoCheckout: true}
	if ref != "" {
		cloneOpts.ReferenceName = plumbing.ReferenceName(ref)
		cloneOpts.SingleBranch = true
	}
	log.Printf("Cloning %s", repo)
	// Clone will use native git if available, otherwise fall back to go-git
	if _, err := s.cloneFunc(ctx, st, nil, cloneOpts); err != nil {
		if ref != "" {
			return errors.Wrapf(err, "failure cloning %s at ref %s", repo, ref)
		}
		return errors.Wrapf(err, "failure cloning %s", repo)
	}
	log.Println("Clone successful")
	w, err := s.backend.writer(ctx, cachePath)
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
