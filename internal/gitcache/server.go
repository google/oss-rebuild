// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/execx"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

var thresholdFudgeFactor = 24 * time.Hour

// A fill stops at fillTimeout, below the service's 60-minute request timeout
// so the request can still report it, or once it stages more than its size
// budget, checked every sizePollInterval.
var (
	fillTimeout      = 55 * time.Minute
	errFillTimeout   = errors.New("fill ran past its time budget")
	sizePollInterval = 5 * time.Second
)

// sizeLimitHeader carries the fill budget on a 413.
const sizeLimitHeader = "Git-Cache-Size-Limit"

// sizeHeader carries the bytes of the archive a redirect points to.
const sizeHeader = "Git-Cache-Size"

// fillRetryAfter is the Retry-After for an entry being filled. Waiting in
// place would hold an instance for each of a monorepo's many packages.
const fillRetryAfter = 10 * time.Second

// Server provides HTTP handling for the git cache.
type Server struct {
	backend   cacheBackend
	cloneFunc gitx.CloneFunc
	remote    func(repo string) string // clone URL. defaults to https
	fillBytes int64                    // fill budget in bytes. 0 means none
}

func (s *Server) remoteURL(repo string) string {
	if s.remote != nil {
		return s.remote(repo)
	}
	return "https://" + repo
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
		held, release, err := s.backend.lease(ctx, p)
		if err != nil {
			if !errors.Is(err, errLeaseHeld) {
				log.Printf("Failed to lease the fill of %s: %v\n", p, err)
			}
			respondFilling(rw)
			return
		}
		defer release()
		// Another fill may have finished between the check and the lease.
		if mtime, err := s.backend.exists(held, p); err != nil || mtime.IsZero() || mtime.Before(threshold) {
			fctx, cancel := context.WithTimeoutCause(held, fillTimeout, errFillTimeout)
			defer cancel()
			if err := s.fill(fctx, u, req.Ref, p, !mtime.IsZero()); err != nil {
				log.Printf("Failed to populate cache: %v\n", err)
				var tooLarge *gitx.RepoTooLargeError
				switch {
				case errors.Is(err, transport.ErrAuthenticationRequired):
					http.Error(rw, err.Error(), 400)
				case errors.Is(context.Cause(held), errLeaseLost):
					// The entry is now the new holder's to write.
					respondFilling(rw)
				case errors.As(err, &tooLarge):
					rw.Header().Set(sizeLimitHeader, strconv.FormatInt(tooLarge.Limit, 10))
					http.Error(rw, err.Error(), http.StatusRequestEntityTooLarge)
				default:
					// The previous entry stays for the next fill to refresh from.
					http.Error(rw, "Fill failed: "+err.Error(), 500)
				}
				return
			}
		}
	}
	s.backend.serve(rw, httpReq, p)
}

// respondFilling answers a request for an entry another request fills, or
// whose lease could not be taken.
func respondFilling(rw http.ResponseWriter) {
	rw.Header().Set("Retry-After", strconv.Itoa(int(fillRetryAfter.Seconds())))
	http.Error(rw, "Entry is being filled", http.StatusServiceUnavailable)
}

// fill refreshes the entry at cachePath by fetch when it can, else clones it.
func (s *Server) fill(ctx context.Context, repo, ref, cachePath string, exists bool) error {
	// A single-ref entry is a single-branch clone, cheap to redo.
	if exists && ref == "" {
		err := s.refreshCache(ctx, repo, cachePath)
		// A clone fails the same way once the budget is spent.
		var tooLarge *gitx.RepoTooLargeError
		if err == nil || ctx.Err() != nil || errors.As(err, &tooLarge) {
			return err
		}
		log.Printf("Refreshing %s by fetch failed, cloning instead: %v\n", cachePath, err)
	}
	return s.populateCache(ctx, repo, ref, cachePath)
}

// nilCache is a no-op cache for git objects.
type nilCache struct{}

func (c nilCache) Get(plumbing.Hash) (plumbing.EncodedObject, bool) { return nil, false }
func (c nilCache) Put(plumbing.EncodedObject)                       {}
func (c nilCache) Clear()                                           {}

// populateCache writes an archived bare checkout of the repo to the cache backend.
func (s *Server) populateCache(ctx context.Context, repo, ref string, cachePath string) (err error) {
	// Create a temp directory for cloning
	tmpDir, err := os.MkdirTemp("", "git-cache-*")
	if err != nil {
		return errors.Wrap(err, "creating temp directory")
	}
	defer os.RemoveAll(tmpDir)
	ctx, stop := s.watchSize(ctx, repo, tmpDir)
	defer stop()
	defer func() { err = fillErr(ctx, err) }()
	// Set up filesystem storage in the temp directory
	m := osfs.New(tmpDir)
	st := filesystem.NewStorage(m, nilCache{})
	cloneOpts := &git.CloneOptions{URL: s.remoteURL(repo), NoCheckout: true}
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
	if err := splitPacks(ctx, tmpDir); err != nil {
		return errors.Wrapf(err, "splitting packs of %s", repo)
	}
	return s.writeEntry(ctx, tmpDir, cachePath)
}

func (s *Server) writeEntry(ctx context.Context, dir, cachePath string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.backend.writer(ctx, cachePath)
	if err != nil {
		return errors.Wrapf(err, "failure opening cache writer for %s", cachePath)
	}
	if err := createTarball(dir, w); err != nil {
		cancel()
		w.Close()
		return errors.Wrapf(err, "failure archiving %s", cachePath)
	}
	if err := w.Close(); err != nil {
		return errors.Wrapf(err, "failure finalizing cache write for %s", cachePath)
	}
	return nil
}

// cloneRefSpecs fetch what a bare clone holds: branches as both refs/heads
// and refs/remotes/origin, and every tag, even one no branch reaches.
var cloneRefSpecs = []string{"+refs/heads/*:refs/heads/*", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"}

// refreshCache updates the stale entry at cachePath by fetching onto it,
// which for a large repository costs a fraction of a clone. The result holds
// what a clone would: the same refs, HEAD and objects.
func (s *Server) refreshCache(ctx context.Context, repo, cachePath string) (err error) {
	if !gitx.NativeGitAvailable() {
		return errors.New("fetching needs native git")
	}
	tmpDir, err := os.MkdirTemp("", "git-cache-*")
	if err != nil {
		return errors.Wrap(err, "creating temp directory")
	}
	defer os.RemoveAll(tmpDir)
	ctx, stop := s.watchSize(ctx, repo, tmpDir)
	defer stop()
	defer func() { err = fillErr(ctx, err) }()
	r, err := s.backend.reader(ctx, cachePath)
	if err != nil {
		return errors.Wrap(err, "opening entry")
	}
	defer r.Close()
	gr, err := gzip.NewReader(r)
	if err != nil {
		return errors.Wrap(err, "reading entry")
	}
	if err := archive.ExtractTar(tar.NewReader(gr), osfs.New(tmpDir), archive.ExtractOptions{}); err != nil {
		return errors.Wrap(err, "extracting entry")
	}
	// A fetch below the unpack limit would land as loose objects, which readers
	// load eagerly. Keep every fetch packed, as a clone is.
	git := func(args ...string) (string, error) {
		var out, stderr bytes.Buffer
		err := execx.NewRealCommandExecutor().Execute(ctx, execx.CommandOptions{
			Output:    &out,
			Stderr:    &stderr,
			Env:       append(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
			KillGroup: true,
		}, "git", append([]string{"--git-dir=" + tmpDir, "-c", "gc.auto=0", "-c", "fetch.unpackLimit=1"}, args...)...)
		if err != nil {
			err = errors.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(stderr.Bytes()))
		}
		return out.String(), err
	}
	url := s.remoteURL(repo)
	got, err := git("config", "remote.origin.url")
	if err != nil {
		return err
	}
	if got = strings.TrimSpace(got); got != url {
		return errors.Errorf("entry tracks %s, not %s", got, url)
	}
	before, err := gitRefs(git)
	if err != nil {
		return err
	}
	log.Printf("Fetching %s onto the cached entry", repo)
	if _, err := git(append([]string{"fetch", "--quiet", "--prune", "--force", "--update-head-ok", "origin"}, cloneRefSpecs...)...); err != nil {
		return err
	}
	head, err := git("ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return err
	}
	branch, ok := strings.CutPrefix(strings.SplitN(head, "\t", 2)[0], "ref: ")
	if !ok {
		return errors.Errorf("remote HEAD names no branch: %q", head)
	}
	if _, err := git("symbolic-ref", "HEAD", branch); err != nil {
		return err
	}
	after, err := gitRefs(git)
	if err != nil {
		return err
	}
	rewritten := false
	for ref, sha := range after {
		// A clone holds no refs beyond what cloneRefSpecs map.
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/remotes/origin/") && !strings.HasPrefix(ref, "refs/tags/") {
			if _, err := git("update-ref", "-d", ref); err != nil {
				return err
			}
			rewritten = true
			continue
		}
		if old, ok := before[ref]; ok && old != sha {
			// A moved tag strands its old tag object even when the commit it
			// named is still an ancestor.
			if strings.HasPrefix(ref, "refs/tags/") {
				rewritten = true
			} else if _, err := git("merge-base", "--is-ancestor", old, sha); err != nil {
				rewritten = true
			}
		}
	}
	for ref := range before {
		if _, ok := after[ref]; !ok {
			rewritten = true
		}
	}
	packs, err := filepath.Glob(filepath.Join(tmpDir, "objects", "pack", "*.pack"))
	if err != nil {
		return err
	}
	if rewritten || len(packs) > maxPacks {
		// A clone has no unreferenced objects, and each fast-forward refresh adds
		// a pack. Repacking drops packed objects and merges packs, and pruning
		// drops loose objects.
		if _, err := git("repack", "-a", "-d", "-n", "-q", fmt.Sprintf("--max-pack-size=%d", maxPackBytes)); err != nil {
			return err
		}
		if _, err := git("prune", "--expire=now"); err != nil {
			return err
		}
	} else if err := splitPacks(ctx, tmpDir); err != nil {
		return errors.Wrapf(err, "splitting packs of %s", repo)
	}
	// A clone has no FETCH_HEAD.
	if err := os.Remove(filepath.Join(tmpDir, "FETCH_HEAD")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeEntry(ctx, tmpDir, cachePath)
}

func gitRefs(git func(...string) (string, error)) (map[string]string, error) {
	out, err := git("for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if ref, sha, ok := strings.Cut(line, " "); ok {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// maxPacks bounds the packs that fast-forward refreshes accumulate before a
// refresh merges them.
var maxPacks = 20

// maxPackBytes bounds each cached pack. Readers built on a 32-bit libgit2
// cannot reach an object past 4 GiB into a pack, and 2 GiB also keeps every
// offset within a signed 32-bit int.
var maxPackBytes int64 = 2 << 30

// splitPacks repacks dir into packs of at most maxPackBytes when any pack is
// larger. Without native git it leaves the packs as cloned.
func splitPacks(ctx context.Context, dir string) error {
	packs, err := filepath.Glob(filepath.Join(dir, "objects", "pack", "*.pack"))
	if err != nil {
		return err
	}
	over := false
	for _, p := range packs {
		if fi, err := os.Stat(p); err == nil && fi.Size() > maxPackBytes {
			over = true
		}
	}
	if !over || !gitx.NativeGitAvailable() {
		return nil
	}
	// Without -f, existing deltas are reused, so this mostly copies objects.
	// -n skips the dumb-HTTP files a clone doesn't have.
	var out bytes.Buffer
	err = execx.NewRealCommandExecutor().Execute(ctx, execx.CommandOptions{Output: &out, KillGroup: true},
		"git", "--git-dir="+dir, "repack", "-a", "-d", "-n", "-q", fmt.Sprintf("--max-pack-size=%d", maxPackBytes))
	if err != nil {
		return errors.Wrapf(err, "repacking: %s", out.Bytes())
	}
	return nil
}

// createTarball creates a gzipped tar archive of the given directory.
func createTarball(srcDir string, w io.Writer) error {
	m := osfs.New(srcDir)
	// Packs are already compressed, so compressing them again costs most of a
	// large fill and saves little. The stream stays gzip for existing readers.
	gw, err := gzip.NewWriterLevel(w, gzip.NoCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gw)
	err = util.Walk(m, "/", func(path string, info fs.FileInfo, err error) error {
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
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
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

// fillErr reports why ctx ended in place of err, since git killed at a
// budget reports only its signal.
func fillErr(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); err != nil && cause != nil {
		return cause
	}
	return err
}

// watchSize returns a context that ends with a RepoTooLargeError once dir
// stages more than the fill budget, and a func to stop watching.
func (s *Server) watchSize(ctx context.Context, repo, dir string) (context.Context, func()) {
	if s.fillBytes <= 0 {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(sizePollInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if dirBytes(dir) > s.fillBytes {
					cancel(&gitx.RepoTooLargeError{URL: "https://" + repo, Limit: s.fillBytes})
					return
				}
			}
		}
	}()
	return ctx, func() {
		close(done)
		cancel(nil)
	}
}

func dirBytes(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
