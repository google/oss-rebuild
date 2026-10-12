// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

var (
	boundaryPatternTpl     = `(^|[^\d])%s([^\d]|$)`
	continuationPatternTpl = `(?i)%s[._-]?(beta|dev|rc|alpha|pre|canary|post)`
)

// MatchTag evaluates whether the given tag is likely to refer to a package version.
func MatchTag(tag, pkg, version string) (strict bool, approx bool) {
	return tagMatcher(pkg, version)(tag)
}

// tagMatcher returns MatchTag for pkg and version, compiling its patterns once.
func tagMatcher(pkg, version string) func(tag string) (strict, approx bool) {
	spellings := versionSpellings(version)
	quoted := make([]string, len(spellings))
	for i, s := range spellings {
		quoted[i] = regexp.QuoteMeta(s)
	}
	anySpelling := "(?:" + strings.Join(quoted, "|") + ")"
	boundaryRE := regexp.MustCompile(fmt.Sprintf(boundaryPatternTpl, anySpelling))
	continuationRE := regexp.MustCompile(fmt.Sprintf(continuationPatternTpl, anySpelling))
	var org string
	if slash := strings.IndexByte(pkg, '/'); slash != -1 {
		org = pkg[:slash]
	}
	return func(tag string) (strict, approx bool) {
		containsOrgButNotPkg := org != "" && strings.Contains(tag, org) && !strings.Contains(tag, pkg)
		strict = boundaryRE.MatchString(tag) && !continuationRE.MatchString(tag) && !containsOrgButNotPkg
		approx = slices.ContainsFunc(spellings, func(s string) bool { return strings.Contains(tag, s) })
		return
	}
}

// versionSpellings returns version and, if it has dots, its underscore form
// used by repositories with CVS or SVN heritage (e.g. rel_2_0_52 for 2.0.52).
func versionSpellings(version string) []string {
	if under := strings.ReplaceAll(version, ".", "_"); under != version {
		return []string{version, under}
	}
	return []string{version}
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
	for _, spelling := range versionSpellings(version) {
		if before, after, found := strings.Cut(tag, spelling); found {
			r := strings.ToLower(strings.Trim(before+after, "-_/.@"))
			return strings.Trim(strings.TrimSuffix(r, "v"), "-_/.@")
		}
	}
	return strings.ToLower(tag)
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

// FindTagMatch searches a repositories tags for a possible version match and returns the commit hash.
func FindTagMatch(pkg, version string, repo *git2.Repository) (commit string, err error) {
	var matches, nearMatches []string
	tags, err := allTags(repo)
	if err != nil {
		return "", err
	}
	match := tagMatcher(pkg, version)
	for _, tag := range tags {
		strict, approx := match(tag)
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

func allTags(repo *git2.Repository) (tags []string, err error) {
	ri, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	err = ri.ForEach(func(ref *git2.Reference) error {
		tags = append(tags, ref.Name().Short())
		return nil
	})
	return tags, err
}

// LoadRepo attempts to either reuse the local or load the remote repo specified in CloneOptions.
//
// If rebuild.RepoCacheClientID is present, a Git cache service will be used
// instead of the remote defined in CloneOptions.URL.
//
// NOTE: Inference callers currently disable submodule recursion. Supporting
// nested submodule discovery only requires cloning submodules here.
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
	if r.Repository, err = gitx.Open(s); err != nil {
		return nil, errors.Wrap(err, "opening with go-git2")
	}
	return r, nil
}

// resolveSubmoduleURL resolves a submodule URL from .gitmodules against
// repoURI (when relative) and canonicalizes it to HTTPS.
func resolveSubmoduleURL(repoURI, rawURL string) string {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return ""
	}
	if (strings.HasPrefix(u, "./") || strings.HasPrefix(u, "../")) && repoURI != "" {
		if base, err := url.Parse(strings.TrimSuffix(strings.TrimRight(repoURI, "/"), ".git")); err == nil && base.Host != "" {
			base.Path = path.Clean(path.Join("/", base.Path, u))
			u = base.String()
		}
	}
	if canon, err := uri.CanonicalizeRepoURI(u); err == nil {
		return canon
	}
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" && parsed.User != nil {
		parsed.User = nil
		parsed.Host = parsed.Hostname()
		if canon, err := uri.CanonicalizeRepoURI(parsed.String()); err == nil {
			return canon
		}
	}
	if rest, ok := strings.CutPrefix(u, "git@"); ok {
		if host, repoPath, ok := strings.Cut(rest, ":"); ok && host != "" && repoPath != "" {
			if canon, err := uri.CanonicalizeRepoURI("https://" + host + "/" + strings.TrimLeft(repoPath, "/")); err == nil {
				return canon
			}
		}
	}
	return u
}

// Submodules returns the sorted, deduplicated Submodules defined in
// .gitmodules that exist as submodule gitlinks in tree.
func Submodules(tree *git2.Tree, repoURI string) []Submodule {
	if tree == nil {
		return nil
	}
	gmFile, err := tree.File(".gitmodules")
	if err != nil {
		return nil
	}
	contents, err := gmFile.Contents()
	if err != nil {
		return nil
	}
	modules := config.NewModules()
	if err := modules.Unmarshal([]byte(contents)); err != nil {
		return nil
	}
	var subs []Submodule
	for _, sub := range modules.Submodules {
		p := path.Clean(strings.TrimPrefix(strings.TrimSpace(sub.Path), "./"))
		if p == "" || p == "." {
			continue
		}
		repo := resolveSubmoduleURL(repoURI, sub.URL)
		if repo == "" {
			continue
		}
		entry, err := tree.FindEntry(p)
		if err == nil && entry.Mode == git2.Submodule {
			subs = append(subs, Submodule{
				Repo: repo,
				Ref:  entry.Hash.String(),
				Dir:  p,
			})
		}
	}
	return MergeSubmodules(subs)
}

// MergeSubmodules returns the sorted, deduplicated union of Submodules across
// lists keyed by Dir, or nil if none exist. When multiple entries share the
// same Dir, earlier non-empty Repo and Ref fields take precedence and missing
// fields are filled from later entries.
func MergeSubmodules(lists ...[]Submodule) []Submodule {
	byDir := make(map[string]Submodule)
	for _, list := range lists {
		for _, sub := range list {
			p := path.Clean(strings.TrimPrefix(strings.TrimSpace(sub.Dir), "./"))
			if p == "" || p == "." {
				continue
			}
			cur, ok := byDir[p]
			if !ok {
				cur = Submodule{Dir: p}
			}
			if cur.Repo == "" && sub.Repo != "" {
				cur.Repo = sub.Repo
			}
			if cur.Ref == "" && sub.Ref != "" {
				cur.Ref = sub.Ref
			}
			byDir[p] = cur
		}
	}
	if len(byDir) == 0 {
		return nil
	}
	out := make([]Submodule, 0, len(byDir))
	for _, sub := range byDir {
		out = append(out, sub)
	}
	slices.SortFunc(out, func(a, b Submodule) int {
		return strings.Compare(a.Dir, b.Dir)
	})
	return out
}
