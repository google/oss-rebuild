// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
	"github.com/pkg/errors"
)

var (
	ecosystem = flag.String("ecosystem", "maven", "package ecosystem")
	pkg       = flag.String("pkg", "", "package identifier")
	version   = flag.String("version", "", "package version")
	repo      = flag.String("repo", "", "package repo")
	repoPath  = flag.String("repo-path", "", "local path from which to load the package repo")
	strategy  = flag.String("strategy", "dynamic", "strategy to use to search and rank commits. {dynamic, commits-near-publish}")
)

type searchStrategy interface {
	Search(ctx context.Context, r *git.Repository, hashes []plumbing.Hash) (closest []string, matched, total int, err error)
}

func getRepo(ctx context.Context, uri, path string) (*git.Repository, error) {
	var s storage.Storer
	if path != "" {
		gitfs, err := osfs.New(path).Chroot(".git")
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
		p, err := mavenreg.HTTPRegistry{}.PackageVersion(ctx, *pkg, *version)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching version metadata"))
		}
		f, err = mavenreg.HTTPRegistry{}.ReleaseFile(ctx, *pkg, *version, mavenreg.TypeSources)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching source jar"))
		}
		defer f.Close()
		published = p.Published
	case "pypi":
		p, err := pypireg.HTTPRegistry{}.Project(ctx, *pkg)
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
	hashes, err := gitscan.BlobHashesFromZip(zr)
	if err != nil {
		log.Fatal(errors.Wrap(err, "hash calculation"))
	}
	var s searchStrategy
	switch *strategy {
	case "dynamic":
		s = &gitscan.LazyTreeCount{}
	case "commits-near-publish":
		s = &gitscan.CommitsNearPublish{Published: published, Window: 7 * 24 * time.Hour}
	case "dynamic-exhaustive":
		s = &gitscan.ExactTreeCount{}
	default:
		log.Fatalln("unknown strategy:", *strategy)
	}
	closest, matched, total, err := s.Search(ctx, r, hashes)
	if err != nil {
		log.Fatal(errors.Wrap(err, "identity search"))
	}
	if matched == 0 {
		log.Fatal(errors.New("no file matches"))
	}
	fmt.Printf("With matches on %d of %d files, best match: %v\n", matched, total, closest)
}
