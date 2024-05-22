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

package rebuild

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	gitinternal "github.com/google/oss-rebuild/internal/git"
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
		return
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
		if len(matches) > 1 {
			log.Printf("Multiple tag matches [pkg=%s,ver=%s,matches=%v]\n", pkg, version, matches)
		}
		ref, err := repo.Tag(matches[0])
		if err != nil {
			return "", err
		}
		if t, err := repo.TagObject(ref.Hash()); err == nil {
			// Annotated tag. Use the Target pointer as the ref hash.
			commit = t.Target.String()
		} else {
			// Lightweight tag. Use the ref hash itself.
			commit = ref.Hash().String()
		}
	}
	return
}

func allTags(repo *git.Repository) (tags []string, err error) {
	ri, err := repo.Tags()
	if err != nil {
		return
	}
	var ref *plumbing.Reference
	for {
		ref, err = ri.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return
		}
		tags = append(tags, ref.Name().Short())
	}
	ti, err := repo.TagObjects()
	if err != nil {
		return
	}
	err = ti.ForEach(func(t *object.Tag) error {
		tags = append(tags, t.Name)
		return nil
	})
	return
}

// LoadRepo attempts to either reuse the local or load the remote repo specified in CloneOptions.
//
// If rebuild.RepoCacheClientID is present, a Git cache service will be used
// instead of the remote defined in CloneOptions.URL.
func LoadRepo(ctx context.Context, pkg string, s storage.Storer, fs billy.Filesystem, opt git.CloneOptions) (*git.Repository, error) {
	var r *git.Repository
	r, err := gitinternal.Reuse(ctx, s, fs, &opt)
	switch err {
	case nil:
		log.Printf("Reusing already cloned repository [pkg=%s]\n", pkg)
	case gitinternal.ErrRemoteNotTracked:
		log.Printf("Cannot reuse already cloned repository [pkg=%s]. Cleaning up...\n", pkg)
		is, ok := s.(*gitinternal.Storer)
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
		if c, ok := ctx.Value(RepoCacheClientID).(*gitinternal.Cache); ok && c != nil {
			r, err = c.Clone(ctx, s, fs, &opt)
			if err != nil {
				return nil, errors.Wrap(err, "using repo cache")
			}
			log.Printf("Using cached repository [pkg=%s]\n", pkg)
		} else {
			r, err = gitinternal.Clone(ctx, s, fs, &opt)
			if err != nil {
				return nil, errors.Wrap(err, "cloning repo")
			}
			log.Printf("Using cloned repository [pkg=%s]\n", pkg)
		}
	default:
		return nil, errors.Wrap(err, "using existing")
	}
	return r, nil
}
