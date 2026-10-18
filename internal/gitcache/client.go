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
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	// MaxBytes cuts a clone off with a gitx.RepoTooLargeError once the cached
	// archive reaches it. 0 fetches whatever the cache serves.
	MaxBytes int64
}

// cappedReader hands out at most left bytes and then fails with err, so
// a fetch stops at the caller's budget instead of at the archive's end.
type cappedReader struct {
	r    io.Reader
	left int64
	err  error
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, c.err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// GetLink returns a GCS link to the cached repo resource.
func (c Client) GetLink(ctx context.Context, repo string, contains time.Time) (uri string, err error) {
	return c.GetLinkWithRef(ctx, repo, contains, "")
}

// GetLinkWithRef returns a GCS link to the cached repo resource for a specific ref.
func (c Client) GetLinkWithRef(ctx context.Context, repo string, contains time.Time, ref string) (uri string, err error) {
	uri, _, err = c.getLink(ctx, repo, contains, ref)
	return uri, err
}

// getLink returns the link and the bytes of the archive it serves, or 0 when missing.
func (c Client) getLink(ctx context.Context, repo string, contains time.Time, ref string) (uri string, size int64, err error) {
	u, err := c.URL.Parse("/get")
	if err != nil {
		return "", 0, err
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
	// Copy the client since requests share it.
	idc := *c.IDClient
	idc.CheckRedirect = func(*http.Request, []*http.Request) error {
		// Never follow redirect.
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := idc.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	var errs []byte
	switch resp.StatusCode {
	case http.StatusFound:
		size, _ = strconv.ParseInt(resp.Header.Get(sizeHeader), 10, 64)
		return resp.Header.Get("Location"), size, nil
	case http.StatusOK:
		return u.String(), max(resp.ContentLength, 0), nil
	case http.StatusBadRequest:
		errs, err = io.ReadAll(resp.Body)
		if err != nil {
			return uri, 0, err
		}
		if strings.Contains(string(errs), transport.ErrAuthenticationRequired.Error()) {
			return uri, 0, transport.ErrAuthenticationRequired
		}
		return uri, 0, errors.New(string(errs))
	case http.StatusServiceUnavailable:
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			return uri, 0, &fillingError{retryAfter: time.Duration(s) * time.Second}
		}
	case http.StatusRequestEntityTooLarge:
		limit, _ := strconv.ParseInt(resp.Header.Get(sizeLimitHeader), 10, 64)
		return uri, 0, &gitx.RepoTooLargeError{URL: repo, Limit: limit}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	body := strings.TrimSpace(string(b))
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		// Waiting out a remote's rate limit would only ask it again.
		return uri, 0, errors.Wrap(gitx.ErrRateLimited, body)
	case http.StatusUnprocessableEntity:
		return uri, 0, errors.Wrap(ErrGaveUp, body)
	case http.StatusGatewayTimeout:
		return uri, 0, ErrFillTimeout // the body only repeats it
	}
	return uri, 0, errors.Errorf("making cache request: %s: %s", resp.Status, body)
}

// ErrGaveUp reports an entry whose fills kept failing, which the cache does
// not fill again for a while.
var ErrGaveUp = errors.New("cache gave up filling the entry")

// ErrFillTimeout reports a fill that ran past the cache's time budget.
var ErrFillTimeout = errors.New("fill ran past its time budget")

type fillingError struct {
	retryAfter time.Duration
}

func (e *fillingError) Error() string {
	return "cache entry is being filled, retry after " + e.retryAfter.String()
}

// waitLink is getLink at the default freshness, retried while the entry fills.
func (c Client) waitLink(ctx context.Context, repo, ref string) (uri string, size int64, err error) {
	uri, size, err = c.getLink(ctx, repo, c.DefaultFreshness, ref)
	var filling *fillingError
	for errors.As(err, &filling) {
		select {
		case <-ctx.Done():
			return "", 0, errors.Wrap(context.Cause(ctx), "waiting for the cache fill")
		case <-time.After(filling.retryAfter):
		}
		uri, size, err = c.getLink(ctx, repo, c.DefaultFreshness, ref)
	}
	return uri, size, err
}

// Size returns the bytes of the archive Clone would fetch for repo, or 0 when
// the cache reports none. Like Clone, it waits out a fill and refuses an
// archive over MaxBytes.
func (c Client) Size(ctx context.Context, repo string) (int64, error) {
	_, size, err := c.waitLink(ctx, repo, "")
	if err != nil {
		return 0, errors.Wrap(err, "querying git cache")
	}
	if c.MaxBytes > 0 && size > c.MaxBytes {
		return 0, &gitx.RepoTooLargeError{URL: repo, Limit: c.MaxBytes, Size: size}
	}
	return size, nil
}

// Clone provides an interface to clone a git repo using the GitCache.
func (c Client) Clone(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*gitx.Repo, error) {
	if opt.Auth != nil || opt.RemoteName != "" || opt.Depth != 0 || opt.RecurseSubmodules != 0 || opt.Tags != git.InvalidTagMode || opt.InsecureSkipTLS || len(opt.CABundle) > 0 {
		// No support for non-trivial opts aside from NoCheckout, ReferenceName, and SingleBranch.
		return nil, errors.New("Unsupported opt")
	}
	// Determine extraction target: use the filesystem directly for
	// filesystem.Storage, otherwise stage via an in-memory storer.
	// NOTE: The staging storer must not be cleaned up as CopyStorer is only a
	// shallow-copy and retains indirect references to the staging storer.
	var extractFS billy.Filesystem
	var stagingStorer *filesystem.Storage
	if sf, ok := s.(*filesystem.Storage); ok {
		extractFS = sf.Filesystem()
	} else {
		stagingStorer = gitx.NewInMemoryStorer()
		extractFS = stagingStorer.Filesystem()
	}
	// Use ref if specified in CloneOptions
	var ref string
	if opt.ReferenceName != "" {
		ref = string(opt.ReferenceName)
	}
	uri, _, err := c.waitLink(ctx, opt.URL, ref)
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
	// NOTE: The repo's FetchedAt reflects the served cache entry's fetch time.
	fetchedAt, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	var body io.Reader = resp.Body
	if c.MaxBytes > 0 {
		// A declared length over budget is refused before any of it is read.
		if resp.ContentLength > c.MaxBytes {
			return nil, &gitx.RepoTooLargeError{URL: opt.URL, Limit: c.MaxBytes, Size: resp.ContentLength}
		}
		body = &cappedReader{r: resp.Body, left: c.MaxBytes, err: &gitx.RepoTooLargeError{URL: opt.URL, Limit: c.MaxBytes}}
	}
	gr, err := gzip.NewReader(body)
	if err != nil {
		return nil, errors.Wrap(err, "gzip read error")
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	// The .git dir contents are at its root, matching the root of extractFS.
	if err := archive.ExtractTar(tr, extractFS, archive.ExtractOptions{}); err != nil {
		return nil, errors.Wrap(err, "tar extract error")
	}
	// Copy staged data into the target storer if needed.
	if stagingStorer != nil {
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
	return &gitx.Repo{Repository: repo, FetchedAt: fetchedAt}, nil
}

var _ gitx.CloneFunc = Client{}.Clone
