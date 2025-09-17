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
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/indexsearch"
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
	strategy  = flag.String("strategy", "dynamic", "strategy to use to search and rank commits. {dynamic, commits-near-publish}")
)

type searchStrategy interface {
	Search(ctx context.Context, r *git.Repository, hashes []string) (closest []string, matched, total int, err error)
}

func main() {
	flag.Parse()
	ctx := context.Background()
	if *repo != "" && *repoPath != "" {
		log.Fatal("-repo and -repo-path are mutually exclusive")
	}
	r, err := indexsearch.GetRepo(ctx, *repo, *repoPath)
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
	hashes, err := indexsearch.ZipHashes(zr)
	if err != nil {
		log.Fatal(errors.Wrap(err, "hash calculation"))
	}
	var s searchStrategy
	switch *strategy {
	case "dynamic":
		s = &indexsearch.DynamicTreeSearchStrategy{}
	case "commits-near-publish":
		s = &indexsearch.CommitsNearPublishStrategy{Published: published, Window: 7 * 24 * time.Hour}
	case "dynamic-exhaustive":
		s = &indexsearch.ExhaustiveTreeSearchStrategy{}
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
