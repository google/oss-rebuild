// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package git

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
	billy "github.com/go-git/go-billy/v5"
	"github.com/pkg/errors"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage"
)

// Cache is an interface abstracting the rebuilder git-cache service.
type Cache struct {
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
func (c Cache) GetLink(repo string, contains time.Time) (uri string, err error) {
	u, err := c.URL.Parse("/get")
	if err != nil {
		return
	}
	q := u.Query()
	q.Add("uri", repo)
	if !contains.IsZero() {
		q.Add("contains", contains.Format(time.RFC3339))
	} else if !c.DefaultFreshness.IsZero() {
		q.Add("contains", c.DefaultFreshness.Format(time.RFC3339))
	}
	u.RawQuery = q.Encode()
	c.IDClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		// Never follow redirect.
		return http.ErrUseLastResponse
	}
	resp, err := c.IDClient.Get(u.String())
	if err != nil {
		return
	}
	var errs []byte
	switch resp.StatusCode {
	case http.StatusFound:
		uri = resp.Header.Get("Location")
		// FIXME: Figure out why this URL parsing artifact is being reintroduced.
		uri = strings.ReplaceAll(uri, "%252F", "%2F")
		return
	case http.StatusBadRequest:
		errs, err = io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		if strings.Contains(string(errs), transport.ErrAuthenticationRequired.Error()) {
			err = transport.ErrAuthenticationRequired
		} else {
			err = errors.New(string(errs))
		}
		return
	default:
		err = errors.Errorf("Request failed: %s", resp.Status)
		return
	}
}

// Clone provides an interface to clone a git repo using the GitCache.
func (c Cache) Clone(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*git.Repository, error) {
	if opt.Auth != nil || opt.RemoteName != "" || opt.ReferenceName != "" || opt.SingleBranch || opt.Depth != 0 || opt.RecurseSubmodules != 0 || opt.Tags != git.InvalidTagMode || opt.InsecureSkipTLS || len(opt.CABundle) > 0 {
		// No support for non-trivial opts aside from NoCheckout.
		return nil, errors.New("Unsupported opt")
	}
	sf, ok := s.(*filesystem.Storage)
	if !ok {
		// Must have access to Filesystem to populate from cache.
		return nil, errors.New("Unsupported Storer")
	}
	sfs := sf.Filesystem()
	uri, err := c.GetLink(opt.URL, c.DefaultFreshness)
	if err != nil {
		return nil, errors.Wrap(err, "cache error")
	}
	resp, err := c.APIClient.Get(uri)
	if err != nil {
		return nil, errors.Wrap(err, "cache storage error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("Failed to fetch cache link: %s", resp.Status)
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "gzip read error")
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	if err := archive.ExtractTar(tr, sfs, archive.ExtractOptions{SubDir: git.GitDirName}); err != nil {
		return nil, errors.Wrap(err, "tar extract error")
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

var _ CloneFunc = Cache{}.Clone
