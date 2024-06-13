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

package uri

import (
	"net/url"
	re "regexp"
	"strings"

	"github.com/pkg/errors"
)

var (
	// NOTE: This is non-exhaustive and should be expanded as necessary.
	githubRE    = re.MustCompile(`(?i)\bgithub(\.com)?[:/]([\w-]+/[\w-\.]+)`)
	gitlabRE    = re.MustCompile(`(?i)\bgitlab(\.com)?[:/]([\w-]+/[\w-\.]+)`)
	bitbucketRE = re.MustCompile(`(?i)\bbitbucket(\.org)?[:/]([\w-]+/[\w-\.]+)`)
	commonRepos = []*re.Regexp{
		githubRE,
		gitlabRE,
		bitbucketRE,
	}
)

var errUnsupportedRepo = errors.Errorf("unsupported repo type")

// CanonicalizeRepoURI parses repos into a canonical HTTPS URI.
func CanonicalizeRepoURI(uri string) (string, error) {
	if uri == "" {
		return "", errors.New("No repo URL")
	}
	var repo string
	// NOTE: For these well-known platforms, ToLower canonicalization is safe.
	if repo = githubRE.FindString(uri); repo != "" {
		repo = "//github.com/" + strings.TrimSuffix(strings.ToLower(repo[strings.IndexAny(repo, ":/")+1:]), ".git")
	} else if repo = gitlabRE.FindString(uri); repo != "" {
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
			return repo
		}
	}
	return ""
}

// SmellsLikeARepo returns true if the uri matches a well known URI pattern.
func SmellsLikeARepo(uri string) bool {
	return FindCommonRepo(uri) != ""
}
