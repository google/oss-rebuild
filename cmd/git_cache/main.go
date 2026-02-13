// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package main implements a git repo cache on GCS.
//
// The served API is as follows:
//
//	/get: Redirect to the GCS repo metadata cache object, populating the cache if necessary.
//	  - uri: Git repo URI e.g. github.com/org/repo
//	  - contains: The RFC3339-formatted time after which a cache entry must have been created.
//	  - ref: Git reference (branch/tag) to cache. If provided, creates a separate cache entry per ref.
//
// # Object Format
//
// The repo cache is stored as a gzipped tar archive of the .git/ directory
// from an empty checkout of the upstream repo.
//
// # Data Races
//
// Racing requests for the same resource will write and return different copies
// of the repo but these are expected to be ~identical and, given the GCS
// object versioning scheme, subsequent requests will converge to return the
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
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/pkg/errors"
)

var (
	bucket = flag.String("bucket", "", "the bucket to use as the git cache")
	port   = flag.Int("port", 8080, "port on which to serve")
)

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
	c, err := storage.NewClient(ctx)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	// Normalize repo URI to provide the following interface:
	// gs://<bucket>/<host>/<org>/<repo>/repo.tgz (default branch)
	// gs://<bucket>/<host>/<org>/<repo>/<ref>/repo.tgz (specific ref)
	var p string
	if r.Ref != "" {
		// Include ref in path to create separate cache entries per ref
		refPath := strings.ReplaceAll(r.Ref, "/", "_") // Replace slashes to avoid path issues
		p = filepath.Join(strings.ToLower(u), refPath, "repo.tgz")
	} else {
		p = filepath.Join(strings.ToLower(u), "repo.tgz")
	}
	o := c.Bucket(*bucket).Object(p)
	a, err := o.Attrs(ctx)
	if err == nil && a.Updated.Before(r.Threshold) {
		// Overwrite cache entry that isn't sufficiently recent.
		log.Printf("Refreshing cache for %s: entry fetched %s before requested %s\n", p, a.Updated.Format(time.RFC3339), r.Threshold.Format(time.RFC3339))
		err = storage.ErrObjectNotExist
	}
	if err != nil {
		switch err {
		case storage.ErrBucketNotExist:
			log.Printf("Configured cache bucket not found: %s\n", *bucket)
			http.Error(rw, "Internal Error", 500)
			return
		case storage.ErrObjectNotExist:
			if err := populateCache(ctx, u, r.Ref, o); err != nil {
				log.Printf("Failed to populate cache: %v\n", err)
				if errors.Is(err, transport.ErrAuthenticationRequired) {
					http.Error(rw, err.Error(), 400)
				} else {
					if err := o.Delete(ctx); err != nil {
						log.Printf("Issue cleaning up failed write: %v\n", err)
					}
					http.Error(rw, "Internal Error", 500)
				}
				return
			}
			a, err = o.Attrs(ctx)
			if err != nil {
				log.Printf("Failed to access metadata for gs://%s/%s: %v\n", o.BucketName(), o.ObjectName(), err)
				http.Error(rw, "Internal Error", 500)
				return
			}
		default:
			log.Printf("Unknown error fetching gs://%s/%s: %v\n", *bucket, p, err)
			http.Error(rw, "Internal Error", 500)
			return
		}
	}
	redirect := url.URL{
		Scheme:   "https",
		Host:     "storage.googleapis.com",
		Path:     fmt.Sprintf("download/storage/v1/b/%s/o/%s", *bucket, url.QueryEscape(p)),
		RawQuery: fmt.Sprintf("generation=%d&alt=media", a.Generation),
	}
	redirect.RawPath = redirect.Path
	http.Redirect(rw, req, redirect.String(), http.StatusFound)
}

// nilCache is a fake local cache for git.
type nilCache struct{}

func (c nilCache) Get(plumbing.Hash) (plumbing.EncodedObject, bool) { return nil, false }
func (c nilCache) Put(plumbing.EncodedObject)                       {}
func (c nilCache) Clear()                                           {}

func doClone(ctx context.Context, mfs billy.Filesystem, cloneOpts *git.CloneOptions) error {
	s := filesystem.NewStorage(mfs, nilCache{})
	_, err := git.CloneContext(ctx, s, nil, cloneOpts)
	return err
}

// populateCache writes an archived bare checkout of the repo to the GCS object.
func populateCache(ctx context.Context, repo, ref string, o *storage.ObjectHandle) error {
	m := memfs.New()
	dotGit, err := m.Chroot(git.GitDirName)
	if err != nil {
		return errors.Wrap(err, "failure allocating .git/")
	}
	cloneOpts := &git.CloneOptions{URL: "https://" + repo, NoCheckout: true}
	if ref != "" {
		// Clone specific ref/branch
		cloneOpts.ReferenceName = plumbing.ReferenceName(ref)
		cloneOpts.SingleBranch = true
	}
	log.Printf("Cloning with opts: %v", cloneOpts)
	if err := doClone(ctx, dotGit, cloneOpts); err != nil {
		if ref != "" {
			return errors.Wrapf(err, "failure cloning %s at ref %s", repo, ref)
		}
		return errors.Wrapf(err, "failure cloning %s", repo)
	}
	log.Println("Clone successful")
	w := o.NewWriter(ctx)
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	var bytesWritten int64
	err = util.Walk(m, m.Root(), func(path string, info fs.FileInfo, err error) error {
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
			// Periodically flush to GCS.
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
		return errors.Wrapf(err, "failure archiving files in %s", repo)
	}
	if err := tw.Close(); err != nil {
		return errors.Wrapf(err, "failure archiving to gs://%s/%s", o.BucketName(), o.ObjectName())
	}
	if err := gw.Close(); err != nil {
		return errors.Wrapf(err, "failure compressing to gs://%s/%s", o.BucketName(), o.ObjectName())
	}
	if err := w.Close(); err != nil {
		return errors.Wrapf(err, "failure uploading to gs://%s/%s", o.BucketName(), o.ObjectName())
	}
	return nil
}

func main() {
	flag.Parse()
	http.HandleFunc("/get", HandleGet)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalln(err)
	}
}
