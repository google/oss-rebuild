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

// Package main implements a repo scanning tool to identify the best ref match for an upstream artifact.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
)

var (
	ecosystem = flag.String("ecosystem", "maven", "package ecosystem")
	pkg       = flag.String("pkg", "", "package identifier")
	version   = flag.String("version", "", "package version")
	repo      = flag.String("repo", "", "package repo")
	repoPath  = flag.String("repo-path", "", "local path from which to load the package repo")
)

func getRepo(ctx context.Context, uri, path string) (*git.Repository, error) {
	var s storage.Storer
	if *repoPath != "" {
		gitfs, err := osfs.New(*repoPath).Chroot(".git")
		if err != nil {
			return nil, errors.Wrap(err, "creating git chroot")
		}
		s = filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: true})
	} else {
		base, err := os.MkdirTemp("", "oss-rebuild")
		if err != nil {
			return nil, errors.Wrap(err, "creating temp dir")
		}
		if err := os.Mkdir(filepath.Join(base, ".git"), os.ModePerm); err != nil {
			return nil, errors.Wrap(err, "creating temp dir")
		}
		gitfs, err := osfs.New(base).Chroot(".git")
		if err != nil {
			return nil, errors.Wrap(err, "creating git chroot")
		}
		s = filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: true})
	}
	if path != "" {
		r, err := git.Open(s, nil)
		return r, errors.Wrap(err, "opening repo")
	}
	r, err := git.CloneContext(ctx, s, nil, &git.CloneOptions{URL: uri, NoCheckout: true})
	return r, errors.Wrap(err, "cloning repo")
}

func zipHashes(ctx context.Context, r *git.Repository, zr *zip.Reader) (files []string, err error) {
	var f io.ReadCloser
	for _, zf := range zr.File {
		size := int64(zf.UncompressedSize64)
		if size < 0 {
			// TODO: support git LFS?
			err = errors.Errorf("file exceeds max supported size: %d", zf.UncompressedSize64)
			return
		}
		h := plumbing.NewHasher(plumbing.BlobObject, size)
		f, err = zf.Open()
		if err != nil {
			return
		}
		if _, err = io.CopyN(h, f, size); err != nil {
			return
		}
		if err = f.Close(); err != nil {
			return
		}
		files = append(files, h.Sum().String())
	}
	return
}

// fileIdentitySearch searches across repository for the input files provided for the nearest matching commit(s).
// The set of matching commits along with the number of matches are returned.
func fileIdentitySearch(ctx context.Context, r *git.Repository, hashes []string, skip func(*object.Commit) bool) (closest []string, matched, total int, err error) {
	files := make(map[string]bool)
	for _, h := range hashes {
		files[h] = true
	}
	total = len(files)
	it, err := r.CommitObjects()
	if err != nil {
		return
	}
	found := make(map[string]bool)
	err = it.ForEach(func(c *object.Commit) error {
		if skip(c) {
			return nil
		}
		t, err := c.Tree()
		if err != nil {
			return err
		}
		clear(found)
		var count int
		tw := object.NewTreeWalker(t, true, nil)
		for {
			_, entry, err := tw.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				panic(err)
			}
			if entry.Mode == filemode.Dir || entry.Mode == filemode.Submodule {
				continue
			}
			hash := entry.Hash.String()
			if files[hash] && !found[hash] {
				count++
				found[hash] = true
			}
		}
		if matched < count {
			matched = count
			closest = closest[:0]
		}
		if matched == count {
			closest = append(closest, c.Hash.String())
		}
		return nil
	})
	return
}

func main() {
	flag.Parse()
	ctx := context.Background()
	if *repo != "" && *repoPath != "" {
		log.Fatal("-repo and -repo-path are mutually exclusive")
	}
	r, err := getRepo(ctx, *repo, *repoPath)
	if err != nil {
		log.Fatal(err)
	}
	var f io.ReadCloser
	var published time.Time
	switch *ecosystem {
	case "maven":
		mv, err := mavenreg.VersionMetadata(*pkg, *version)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching version metadata"))
		}
		f, err = mavenreg.ReleaseFile(mv.GroupID+":"+mv.ArtifactID, mv.Version, mavenreg.TypeSources)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching source jar"))
		}
		defer f.Close()
		published = mv.Published
	case "pypi":
		p, err := pypireg.HTTPRegistry{}.Project(*pkg)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching pypi metadata"))
		}
		for _, rel := range p.Releases[*version] {
			if strings.HasSuffix(rel.Filename, "-none-any.whl") {
				resp, err := http.Get(rel.URL)
				if err != nil {
					log.Fatal(errors.Wrap(err, "requesting wheel file"))
				}
				if resp.StatusCode != 200 {
					log.Fatal(errors.Wrap(errors.New(resp.Status), "fetching wheel file"))
				}
				f = resp.Body
				defer f.Close()
				published = rel.UploadTime
				break
			}
		}
		if f == nil {
			log.Fatal("artifact not found")
		}
	default:
		log.Fatal(errors.Errorf("unknown ecosystem: %s", *ecosystem))
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(errors.Wrap(err, "reading artifact"))
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		log.Fatal(errors.Wrap(err, "creating zip reader"))
	}
	hashes, err := zipHashes(ctx, r, zr)
	if err != nil {
		log.Fatal(errors.Wrap(err, "hash calculation"))
	}
	wiggleRoom := 7 * 24 * time.Hour
	notNearPublishDate := func(c *object.Commit) bool {
		committed := c.Committer.When
		return committed.Before(published.Add(-1*wiggleRoom)) || committed.After(published.Add(wiggleRoom))
	}
	closest, matched, total, err := fileIdentitySearch(ctx, r, hashes, notNearPublishDate)
	if err != nil {
		log.Fatal(errors.Wrap(err, "identity search"))
	}
	if matched == 0 {
		log.Fatal(errors.New("no file matches"))
	}
	fmt.Printf("With matches on %d of %d files, best match: %v\n", matched, total, closest)
}
