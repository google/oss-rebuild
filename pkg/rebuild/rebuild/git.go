// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/pkg/errors"
)

var (
	boundaryPatternTpl     = `(^|[^\d])%s([^\d]|$)`
	continuationPatternTpl = `%s[\.-]?(beta|dev|rc|alpha|preview|canary)`
)

// MatchTag evaluates whether the given tag is likely to refer to a package version.
func MatchTag(tag, pkg, version string) (strict bool, approx bool) {
	boundaryRE := regexp.MustCompile(fmt.Sprintf(boundaryPatternTpl, version))
	continuationRE := regexp.MustCompile(fmt.Sprintf(continuationPatternTpl, version))
	var org string
	if slash := strings.IndexByte(pkg, '/'); slash != -1 {
		org = pkg[:slash]
	}
	containsOrgButNotPkg := org != "" && strings.Contains(tag, org) && !strings.Contains(tag, pkg)
	strict = boundaryRE.MatchString(tag) && !continuationRE.MatchString(tag) && !containsOrgButNotPkg
	approx = strings.Contains(tag, version)
	return
}

// FindTagMatch searches a repositories tags for a possible version match and returns the commit hash.
func FindTagMatch(pkg, version string, repo *git.Repository) (commit string, err error) {
	var matches, nearMatches []string
	tags, err := allTags(repo)
	if err != nil {
		return "", err
	}
	for _, tag := range tags {
		strict, approx := MatchTag(tag, pkg, version)
		if strict {
			matches = append(matches, tag)
		} else if approx {
			nearMatches = append(nearMatches, tag)
		}
	}
	if len(nearMatches) > 0 {
		log.Printf("Rejected potential matches [pkg=%s,ver=%s,matches=%v]\n", pkg, version, nearMatches)
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		if len(matches) > 1 {
			log.Printf("Multiple tag matches [pkg=%s,ver=%s,matches=%v]\n", pkg, version, matches)
		}
		ref, err := repo.Tag(matches[0])
		if err != nil {
			return "", err
		}
		if t, err := repo.TagObject(ref.Hash()); err == nil {
			// Annotated tag. Use the Target pointer as the ref hash.
			return t.Target.String(), nil
		} else {
			// Lightweight tag. Use the ref hash itself.
			return ref.Hash().String(), nil
		}
	}
	return "", nil
}

func allTags(repo *git.Repository) (tags []string, err error) {
	ri, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	for ref, err := range iterx.ToSeq2(ri, io.EOF) {
		if err != nil {
			return nil, err
		}
		tags = append(tags, ref.Name().Short())
	}
	return tags, nil
}

// LoadRepo attempts to either reuse the local or load the remote repo specified in CloneOptions.
//
// If rebuild.RepoCacheClientID is present, a Git cache service will be used
// instead of the remote defined in CloneOptions.URL.
func LoadRepo(ctx context.Context, pkg string, s storage.Storer, fs billy.Filesystem, opt git.CloneOptions) (*git.Repository, error) {
	var r *git.Repository
	r, err := gitx.Reuse(ctx, s, fs, &opt)
	switch err {
	case nil:
		log.Printf("Reusing already cloned repository [pkg=%s,repoURL=%s]\n", pkg, opt.URL)
	case gitx.ErrRemoteNotTracked:
		log.Printf("Cannot reuse already cloned repository [pkg=%s,repoURL=%s]. Cleaning up...\n", pkg, opt.URL)
		is, ok := s.(*gitx.Storer)
		if !ok {
			return nil, errors.New("cleaning up unsupported Storer")
		}
		fss, ok := is.Storer.(*filesystem.Storage)
		if !ok {
			return nil, errors.New("cleaning up unsupported Storer")
		}
		sfs := fss.Filesystem()
		if err := util.RemoveAll(sfs, "/"); err != nil {
			return nil, errors.Wrap(err, "cleaning up existing metadata")
		}
		is.Reset()
		if err := util.RemoveAll(fs, "/"); err != nil {
			return nil, errors.Wrap(err, "cleaning up existing")
		}
		fallthrough
	case git.ErrRepositoryNotExists:
		if c, ok := ctx.Value(RepoCacheClientID).(*gitcache.Client); ok && c != nil {
			r, err = c.Clone(ctx, s, fs, &opt)
			if err != nil {
				return nil, errors.Wrap(err, "using repo cache")
			}
			log.Printf("Using cached repository [pkg=%s,repoURL=%s]\n", pkg, opt.URL)
		} else {
			r, err = gitx.Clone(ctx, s, fs, &opt)
			if err != nil {
				return nil, errors.Wrap(err, "cloning repo")
			}
			log.Printf("Using cloned repository [pkg=%s,repoURL=%s]\n", pkg, opt.URL)
		}
	default:
		return nil, errors.Wrap(err, "using existing")
	}
	return r, nil
}
