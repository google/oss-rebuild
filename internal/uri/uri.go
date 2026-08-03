// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package uri

import (
	"net/url"
	re "regexp"
	"slices"
	"strings"

	"github.com/pkg/errors"
)

var (
	// NOTE: This is non-exhaustive and should be expanded as necessary.
	githubRE    = re.MustCompile(`(?i)\bgithub(\.com)?[:/]([\w-]+/[\w-\.]+)`)
	gitlabRE    = re.MustCompile(`(?i)\bgitlab(\.com)?[:/]([\w-]+(?:/[\w.-]+)+)`)
	bitbucketRE = re.MustCompile(`(?i)\bbitbucket(\.org)?[:/]([\w-]+/[\w-\.]+)`)
	commonRepos = []*re.Regexp{
		githubRE,
		gitlabRE,
		bitbucketRE,
	}
	// Project routes that directly follow the project path. GitLab has served
	// these under the /-/ prefix since 12.0 but older links omit it.
	// NOTE: This is non-exhaustive and should be expanded as necessary.
	gitlabRoutes = []string{
		"badges", "blob", "commits", "container_registry", "issues", "packages",
		"pipelines", "raw", "releases", "tags", "tree", "uploads", "wikis",
	}
)

var errUnsupportedRepo = errors.Errorf("unsupported repo type")

// trimGitLabRoute drops any project route from a matched GitLab path.
func trimGitLabRoute(repo string) string {
	if root, _, found := strings.Cut(repo, "/-/"); found {
		return root
	}
	pathStart := strings.IndexAny(repo, ":/")
	if pathStart < 0 {
		return repo
	}
	segments := strings.Split(repo[pathStart+1:], "/")
	for i := 2; i < len(segments); i++ {
		if slices.Contains(gitlabRoutes, segments[i]) {
			return repo[:pathStart+1] + strings.Join(segments[:i], "/")
		}
	}
	return repo
}

// CanonicalizeRepoURI parses repos into a canonical HTTPS URI.
func CanonicalizeRepoURI(uri string) (string, error) {
	if uri == "" {
		return "", errors.New("No repo URL")
	}
	var repo string
	// NOTE: For these well-known platforms, ToLower canonicalization is safe.
	if repo = githubRE.FindString(uri); repo != "" {
		repo = "//github.com/" + strings.TrimSuffix(strings.ToLower(repo[strings.IndexAny(repo, ":/")+1:]), ".git")
	} else if repo = trimGitLabRoute(gitlabRE.FindString(uri)); repo != "" {
		repo = "//gitlab.com/" + strings.TrimSuffix(strings.ToLower(repo[strings.IndexAny(repo, ":/")+1:]), ".git")
	} else if repo = bitbucketRE.FindString(uri); repo != "" {
		repo = "//bitbucket.org/" + strings.TrimSuffix(strings.ToLower(repo[strings.IndexAny(repo, ":/")+1:]), ".git")
	} else {
		// Try to parse it as a URL and see what happens.
		repo = uri
	}
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" || u.User.String() != "" {
		return "", errors.Wrap(errUnsupportedRepo, uri)
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	if strings.HasSuffix(u.Path, "/.") || strings.HasSuffix(u.Path, "/..") {
		return "", errors.Wrap(errUnsupportedRepo, uri)
	}
	u.RawQuery = ""
	return u.String(), nil
}

// FindCommonRepo attempts to find something that looks like a repo in the text. It will return empty string when no repo is found.
func FindCommonRepo(text string) string {
	for _, pattern := range commonRepos {
		if repo := pattern.FindString(text); repo != "" {
			if pattern == gitlabRE {
				repo = trimGitLabRoute(repo)
			}
			return repo
		}
	}
	return ""
}
