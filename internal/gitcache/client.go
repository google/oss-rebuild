// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

// Client is an interface abstracting the rebuilder git-cache service.
type Client struct {
	// IDClient is the HTTP client to use to access the cache service.
	IDClient *http.Client
	// APIClient is the HTTP client to use to access the underlying cache storage.
	APIClient *http.Client
	// URL is the address of the cache service.
	*url.URL
	// DefaultFreshness is the freshness bound to use if none is provided.
	DefaultFreshness time.Time
}

// GetLink returns a GCS link to the cached repo resource.
func (c Client) GetLink(repo string, contains time.Time) (uri string, err error) {
	return c.GetLinkWithRef(repo, contains, "")
}

// GetLinkWithRef returns a GCS link to the cached repo resource for a specific ref.
func (c Client) GetLinkWithRef(repo string, contains time.Time, ref string) (uri string, err error) {
	u, err := c.URL.Parse("/get")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Add("uri", repo)
	if !contains.IsZero() {
		q.Add("contains", contains.Format(time.RFC3339))
	} else if !c.DefaultFreshness.IsZero() {
		q.Add("contains", c.DefaultFreshness.Format(time.RFC3339))
	}
	if ref != "" {
		q.Add("ref", ref)
	}
	u.RawQuery = q.Encode()
	c.IDClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		// Never follow redirect.
		return http.ErrUseLastResponse
	}
	resp, err := c.IDClient.Get(u.String())
	if err != nil {
		return "", err
	}
	var errs []byte
	switch resp.StatusCode {
	case http.StatusFound:
		uri = resp.Header.Get("Location")
		// FIXME: Figure out why this URL parsing artifact is being reintroduced.
		return strings.ReplaceAll(uri, "%252F", "%2F"), nil
	case http.StatusBadRequest:
		errs, err = io.ReadAll(resp.Body)
		if err != nil {
			return uri, err
		}
		if strings.Contains(string(errs), transport.ErrAuthenticationRequired.Error()) {
			return uri, transport.ErrAuthenticationRequired
		}
		return uri, errors.New(string(errs))
	default:
		return uri, errors.Wrap(errors.New(resp.Status), "making cache request")
	}
}

// Clone provides an interface to clone a git repo using the GitCache.
func (c Client) Clone(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*git.Repository, error) {
	if opt.Auth != nil || opt.RemoteName != "" || opt.Depth != 0 || opt.RecurseSubmodules != 0 || opt.Tags != git.InvalidTagMode || opt.InsecureSkipTLS || len(opt.CABundle) > 0 {
		// No support for non-trivial opts aside from NoCheckout, ReferenceName, and SingleBranch.
		return nil, errors.New("Unsupported opt")
	}
	// Determine extraction target: use the filesystem directly for
	// filesystem.Storage, otherwise stage via a temp directory.
	var extractFS billy.Filesystem
	var needsStaging bool
	if sf, ok := s.(*filesystem.Storage); ok {
		extractFS = sf.Filesystem()
	} else {
		needsStaging = true
		tmpDir, err := os.MkdirTemp("", "gitcache-clone-*")
		if err != nil {
			return nil, errors.Wrap(err, "creating staging directory")
		}
		defer os.RemoveAll(tmpDir)
		extractFS = osfs.New(tmpDir)
	}
	// Use ref if specified in CloneOptions
	var ref string
	if opt.ReferenceName != "" {
		ref = string(opt.ReferenceName)
	}
	uri, err := c.GetLinkWithRef(opt.URL, c.DefaultFreshness, ref)
	if err != nil {
		return nil, errors.Wrap(err, "cache error")
	}
	resp, err := c.APIClient.Get(uri)
	if err != nil {
		return nil, errors.Wrap(err, "cache storage error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching cached repo")
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "gzip read error")
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	if err := archive.ExtractTar(tr, extractFS, archive.ExtractOptions{SubDir: git.GitDirName}); err != nil {
		return nil, errors.Wrap(err, "tar extract error")
	}
	// Copy staged data into the target storer if needed.
	if needsStaging {
		stagingStorer := filesystem.NewStorage(extractFS, cache.NewObjectLRUDefault())
		if err := gitx.CopyStorer(s, stagingStorer); err != nil {
			return nil, errors.Wrap(err, "copying from staging to storage")
		}
	}
	repo, err := git.Open(s, fs)
	if err != nil {
		return nil, errors.Wrap(err, "git open error")
	}
	if !opt.NoCheckout {
		wt, err := repo.Worktree()
		if err != nil {
			return nil, errors.Wrap(err, "error loading worktree")
		}
		if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.HEAD}); err != nil {
			return nil, errors.Wrap(err, "checkout error")
		}
	}
	return repo, nil
}

var _ gitx.CloneFunc = Client{}.Clone
