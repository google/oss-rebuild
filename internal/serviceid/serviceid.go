// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package serviceid provides helpers for parsing and canonicalizing build identifiers
// from a repository URI and a Go module pseudo-version.
//
// For release binaries, it is recommended to embed this information at build time
// by setting package variables with -ldflags. For example declaring variables
// in your main package:
//
//	var (
//		buildRepo    string
//		buildVersion string
//	)
//
// ...and building in the values like so:
//
//	$ go build -ldflags "-X main.buildRepo=... -X main.buildVersion=..."
package serviceid

import (
	"net/url"
	"regexp"

	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var goPseudoVersion = regexp.MustCompile(`^v0\.0\.0-\d{14}-[a-f0-9]{12}$`)

func ParseLocation(repo, version string) (rebuild.Location, error) {
	if repo == "" {
		return rebuild.Location{}, errors.New("empty repo")
	}
	repoURI, err := url.Parse(repo)
	if err != nil {
		return rebuild.Location{}, errors.Wrap(err, "parsing repo URI")
	}
	var serviceRepo string
	switch repoURI.Scheme {
	case "file":
		serviceRepo = repoURI.String()
	case "http", "https":
		if canonicalized, err := uri.CanonicalizeRepoURI(repo); err != nil {
			serviceRepo = repoURI.String()
		} else {
			serviceRepo = canonicalized
		}
	default:
		return rebuild.Location{}, errors.Errorf("unsupported scheme for repo '%s'", repo)
	}
	if !goPseudoVersion.MatchString(version) {
		return rebuild.Location{}, errors.New("version must be a go mod pseudo-version: https://go.dev/ref/mod#pseudo-versions")
	}
	return rebuild.Location{
		Repo: serviceRepo,
		Ref:  version,
	}, nil
}
