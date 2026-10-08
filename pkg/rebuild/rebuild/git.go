// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"slices"
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
	escapedVersion := regexp.QuoteMeta(version)
	boundaryRE := regexp.MustCompile(fmt.Sprintf(boundaryPatternTpl, escapedVersion))
	continuationRE := regexp.MustCompile(fmt.Sprintf(continuationPatternTpl, escapedVersion))
	var org string
	if slash := strings.IndexByte(pkg, '/'); slash != -1 {
		org = pkg[:slash]
	}
	containsOrgButNotPkg := org != "" && strings.Contains(tag, org) && !strings.Contains(tag, pkg)
	strict = boundaryRE.MatchString(tag) && !continuationRE.MatchString(tag) && !containsOrgButNotPkg
	approx = strings.Contains(tag, version)
	return
}

// tagAliases provides probable tag names for pkg accounting for e.g. npm @scope/, Maven group:artifact.
func tagAliases(pkg string) []string {
	aliases := []string{strings.ToLower(pkg)}
	if i := strings.LastIndexAny(pkg, "/:"); i >= 0 && i+1 < len(pkg) {
		aliases = append(aliases, strings.ToLower(pkg[i+1:]))
	}
	if t, ok := strings.CutPrefix(pkg, "@"); ok {
		aliases = append(aliases, strings.ToLower(t))
	}
	return aliases
}

// tagResidue is what a tag contains besides the version.
func tagResidue(tag, version string) string {
	before, after, found := strings.Cut(tag, version)
	if !found {
		return strings.ToLower(tag)
	}
	r := strings.ToLower(strings.Trim(before+after, "-_/.@"))
	return strings.Trim(strings.TrimSuffix(r, "v"), "-_/.@")
}

// tagRank measures how strongly a tag appears to relate to this package's release.
func tagRank(tag, version string, aliases []string) int {
	residue := tagResidue(tag, version)
	if slices.Contains(aliases, residue) {
		return 0 // contains package name alias AND version
	}
	if residue == "" {
		return 1 // version alone. correct when the repo holds one package
	}
	for _, a := range aliases {
		if strings.Contains(residue, a) || strings.Contains(a, residue) {
			return 2 // partial alias match. correct when e.g. monorepo uses lang tag prefix like py-<pkg>-v<version>
		}
	}
	return 3 // names something else
}

// sortTagMatches orders candidates by tagRank.
func sortTagMatches(matches []string, pkg, version string) {
	aliases := tagAliases(pkg)
	sort.Strings(matches)
	sort.SliceStable(matches, func(i, j int) bool {
		return tagRank(matches[i], version, aliases) < tagRank(matches[j], version, aliases)
	})
}

func matchingTags(pkg, version string, repo *git.Repository) (matches []string, err error) {
	var nearMatches []string
	tags, err := allTags(repo)
	if err != nil {
		return nil, err
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
		sortTagMatches(matches, pkg, version)
		if len(matches) > 1 {
			log.Printf("Multiple tag matches [pkg=%s,ver=%s,matches=%v]\n", pkg, version, matches)
		}
	}
	return matches, nil
}

func resolveTagMatch(match string, repo *git.Repository) (string, error) {
	ref, err := repo.Tag(match)
	if err != nil {
		return "", err
	}
	if t, err := repo.TagObject(ref.Hash()); err == nil {
		// Annotated tag. Use the Target pointer as the ref hash.
		return t.Target.String(), nil
	}
	// Lightweight tag. Use the ref hash itself.
	return ref.Hash().String(), nil
}

// FindTagMatches searches a repository's tags for possible version matches and returns their commit hashes.
func FindTagMatches(pkg, version string, repo *git.Repository) (commits []string, err error) {
	matches, err := matchingTags(pkg, version, repo)
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		commit, err := resolveTagMatch(match, repo)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

// FindTagMatch searches a repository's tags for a possible version match and returns the first commit hash.
func FindTagMatch(pkg, version string, repo *git.Repository) (commit string, err error) {
	matches, err := matchingTags(pkg, version, repo)
	if err != nil {
		return "", err
	}
	if len(matches) > 0 {
		return resolveTagMatch(matches[0], repo)
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
func LoadRepo(ctx context.Context, pkg string, s storage.Storer, fs billy.Filesystem, opt git.CloneOptions) (*gitx.Repo, error) {
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
