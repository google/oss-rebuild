// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package netclassify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

// OCI
var (
	// ociRegistries contains the URL prefixes of known OCI registries
	ociRegistries = []*regexp.Regexp{
		regexp.MustCompile(`^https://registry-1\.docker\.io/`),
		regexp.MustCompile(`^https://production\.cloudflare\.docker\.com/registry-v2/docker/registry/`),
	}
	// Standard OCI API
	ociManifestRegex = regexp.MustCompile(`/v2/(?P<image>(?:\w+/)?[^/]+)/manifests/(?P<id>[^/]+)$`)
	ociBlobRegex     = regexp.MustCompile(`/v2/(?P<image>(?:\w+/)?[^/]+)/blobs/(?P<id>[^:]+:[a-f0-9]+)`)

	dockerRegex = regexp.MustCompile(`^https://registry-1\.docker\.io/v2/(?P<image>(?:\w+/)?[^/]+)/manifests/(?P<id>[^/]+)$`)
	alpineRegex = regexp.MustCompile(`^https://dl-cdn\.alpinelinux\.org/alpine/(?P<tree>[^/]+)/(?P<repo>main|community|testing)/(?<arch>[^/]+)/(?P<package>[^-]+)-(?P<version>[^-]+-r\d+)\.apk$`)
)

// PyPI
var (
	pypiAPIRegex  = regexp.MustCompile(`^https://pypi\.org/simple/(?P<package>[^/]+)/$`)
	pypiFileRegex = regexp.MustCompile(`^https://files\.pythonhosted\.org/packages/\w{2}/\w{2}/\w{60}/(?P<file>[^/]+)$`)
	// https://peps.python.org/pep-0491/#file-name-convention
	pythonWheelRegex = regexp.MustCompile(`^(?P<package>[\w\.]+)-(?P<version>[^-]+?)(-(?P<build>[^-]+?))?-(?P<python>[^-]+)-(?P<abi>[^-]+)-(?P<platform>[^-]+)\.whl$`)
	// https://packaging.python.org/en/latest/discussions/package-formats/
	pythonSourceRegex = regexp.MustCompile(`^(?P<package>[\w\.]+)-(?P<version>.+?)(?P<ext>\.(zip|tar\.gz|tar\.bz2|tar\.xz|tar\.Z|tar))$`)
)

// GCS
var (
	// https://cloud.google.com/storage/docs/json_api
	gcsJSONRegex = regexp.MustCompile(`^https://storage.googleapis.com/(download/)?storage/v\w+/b/(?P<bucket>[^/]+)/o/(?P<object>.+)$`)
	// https://cloud.google.com/storage/docs/xml-api
	gcsXMLRegex = regexp.MustCompile(`^https://(?P<bucket>[^.]+).storage.googleapis.com/(?P<object>.+)$`)
)

// git
var (
	// gitHosts contains regexp patterns for known Git hosts
	gitHosts = []*regexp.Regexp{
		regexp.MustCompile(`^https://github\.com/(?P<repo>[^/]+/[^/]+)`),
	}
	// Standard Git API endpoint patterns
	gitRefsRegex        = regexp.MustCompile(`^/info/refs$`)
	gitUploadPackRegex  = regexp.MustCompile(`^/git-upload-pack$`)
	gitReceivePackRegex = regexp.MustCompile(`^/git-receive-pack$`)
	gitHeadRegex        = regexp.MustCompile(`^/HEAD$`)
	gitObjectInfoRegex  = regexp.MustCompile(`^/objects/info/(packs|alternates)$`)
	gitObjectPackRegex  = regexp.MustCompile(`^/objects/pack/pack-(?P<digest>[a-f0-9]{40}|[a-f0-9]{64})\.(?:idx|pack)$`)
	gitObjectRegex      = regexp.MustCompile(`^/objects/[a-f0-9]{2}/(?P<digeststub>[a-f0-9]{38}|[a-f0-9]{62})$`)
)

// misc
var (
	dockerAPITokenURL = "https://auth.docker.io/token"
)

var (
	ErrSkipped      = errors.New("URL skipped")
	ErrUnclassified = errors.New("no known classifier")
	ErrBadPySource  = errors.New("bad python source format")
	ErrBadPyWheel   = errors.New("bad python wheel format")
)

func ClassifyURL(rawURL string) (string, error) {
	if pat := matchOCIRegistry(rawURL); pat != nil {
		return classifyOCIRegistryURL(rawURL, pat)
	} else if pat := matchGitHost(rawURL); pat != nil {
		return classifyGitURL(rawURL, pat)
	} else if alpineRegex.MatchString(rawURL) {
		return classifyAlpineURL(rawURL)
	} else if pypiFileRegex.MatchString(rawURL) {
		return classifyPyPIURL(rawURL)
	} else if pypiAPIRegex.MatchString(rawURL) {
		return "", ErrSkipped
	} else if gcsJSONRegex.MatchString(rawURL) {
		return classifyGCSURL(rawURL, gcsJSONRegex)
	} else if gcsXMLRegex.MatchString(rawURL) {
		return classifyGCSURL(rawURL, gcsXMLRegex)
	} else if rawURL == dockerAPITokenURL {
		return "", ErrSkipped
	} else {
		return "", ErrUnclassified
	}
}

func matchOCIRegistry(url string) *regexp.Regexp {
	for _, regPattern := range ociRegistries {
		if regPattern.MatchString(url) {
			return regPattern
		}
	}
	return nil
}

func classifyOCIRegistryURL(rawURL string, registry *regexp.Regexp) (string, error) {
	loc := registry.FindStringIndex(rawURL)
	if loc == nil {
		return "", errors.New("invalid registry pattern")
	}
	part := rawURL[loc[1]-1:]
	switch {
	case ociManifestRegex.MatchString(part):
		matches := ociManifestRegex.FindStringSubmatch(part)
		image, tag := matches[1], matches[2]
		return fmt.Sprintf("pkg:docker/%s@%s", image, tag), nil
	case ociBlobRegex.MatchString(part):
		matches := ociBlobRegex.FindStringSubmatch(part)
		image, digest := matches[1], matches[2]
		return fmt.Sprintf("pkg:docker-blob/%s@%s", image, digest), nil
	default:
		return "", ErrSkipped
	}
}

func matchGitHost(url string) *regexp.Regexp {
	for _, hostPattern := range gitHosts {
		if hostPattern.MatchString(url) {
			return hostPattern
		}
	}
	return nil
}

func classifyGitURL(rawURL string, host *regexp.Regexp) (string, error) {
	loc := host.FindStringIndex(rawURL)
	if loc == nil {
		return "", errors.New("invalid git host pattern")
	}
	part := rawURL[loc[1]:]
	switch {
	case gitRefsRegex.MatchString(part):
		return "", ErrSkipped
	case gitReceivePackRegex.MatchString(part):
		return "", ErrSkipped
	case gitHeadRegex.MatchString(part):
		return "", ErrSkipped
	case gitObjectInfoRegex.MatchString(part):
		return "", ErrSkipped
	case gitUploadPackRegex.MatchString(part):
		fallthrough
	case gitObjectRegex.MatchString(part):
		fallthrough
	case gitObjectPackRegex.MatchString(part):
		matches := host.FindStringSubmatch(rawURL)
		repo := matches[host.SubexpIndex("repo")]
		// TODO: Change from github when other supported hosts.
		return fmt.Sprintf("pkg:github/%s", repo), nil
	default:
		return "", ErrSkipped
	}
}

func classifyAlpineURL(rawURL string) (string, error) {
	matches := alpineRegex.FindStringSubmatch(rawURL)
	if matches == nil {
		return "", fmt.Errorf("invalid Alpine URL format")
	}
	return fmt.Sprintf("pkg:alpine/%s@%s", matches[alpineRegex.SubexpIndex("package")], matches[alpineRegex.SubexpIndex("version")]), nil
}

func classifyPyPIURL(rawURL string) (string, error) {
	matches := pypiFileRegex.FindStringSubmatch(rawURL)
	if matches == nil {
		return "", fmt.Errorf("invalid PyPI URL format")
	}
	return classifyPyPIFile(matches[pypiFileRegex.SubexpIndex("file")])
}

func classifyPyPIFile(fname string) (string, error) {
	switch {
	case strings.HasSuffix(fname, ".metadata"):
		return "", ErrSkipped
	case strings.HasSuffix(fname, ".egg"):
		return "", ErrUnclassified // TODO: Revisit support if this is observed to be sufficiently common.
	case strings.HasSuffix(fname, ".whl"):
		matches := pythonWheelRegex.FindStringSubmatch(fname)
		if matches == nil {
			return "", ErrBadPyWheel
		}
		// NOTE: Case and hyphens may differ from PyPI.
		// TODO: Add file name to pURL.
		return fmt.Sprintf("pkg:pypi/%s@%s", matches[pythonWheelRegex.SubexpIndex("package")], matches[pythonWheelRegex.SubexpIndex("version")]), nil
	default:
		matches := pythonSourceRegex.FindStringSubmatch(fname)
		if matches == nil {
			return "", ErrBadPySource
		}
		// NOTE: Case and hyphens may differ from PyPI.
		// TODO: Add file name to pURL.
		return fmt.Sprintf("pkg:pypi/%s@%s", matches[pythonSourceRegex.SubexpIndex("package")], matches[pythonSourceRegex.SubexpIndex("version")]), nil
	}
}

func classifyGCSURL(rawURL string, pattern *regexp.Regexp) (string, error) {
	matches := pattern.FindStringSubmatch(rawURL)
	bucket, object := matches[pattern.SubexpIndex("bucket")], matches[pattern.SubexpIndex("object")]
	return fmt.Sprintf("pkg:generic/gcs/%s/%s", bucket, object), nil
}
