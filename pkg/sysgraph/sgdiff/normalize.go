// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgdiff

import (
	"regexp"
	"strings"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// NormalizationRule defines a filter for expected differences.
type NormalizationRule interface {
	// ShouldNormalize returns true if this difference should be filtered.
	ShouldNormalize(old, new *sgpb.Resource) bool
	// Description explains why this was normalized.
	Description() string
}

// DefaultNormalizationRules returns the standard set of normalization rules.
func DefaultNormalizationRules() []NormalizationRule {
	return []NormalizationRule{
		&CompiledArtifactNormalizer{},
		&LockfileNormalizer{},
		&BuildPathNormalizer{},
		&SourceMapNormalizer{},
		&PycacheNormalizer{},
	}
}

// CompiledArtifactNormalizer filters compiled artifacts that are expected to change.
type CompiledArtifactNormalizer struct{}

var compiledArtifactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.o$`),
	regexp.MustCompile(`\.a$`),
	regexp.MustCompile(`\.so$`),
	regexp.MustCompile(`\.dylib$`),
	regexp.MustCompile(`\.pyc$`),
	regexp.MustCompile(`\.pyo$`),
	regexp.MustCompile(`\.class$`),
	regexp.MustCompile(`\.jar$`),
	regexp.MustCompile(`\.wasm$`),
}

func (n *CompiledArtifactNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	for _, pattern := range compiledArtifactPatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func (n *CompiledArtifactNormalizer) Description() string {
	return "compiled artifact"
}

// LockfileNormalizer filters lockfile changes as expected.
type LockfileNormalizer struct{}

var lockfileNames = []string{
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"Cargo.lock",
	"go.sum",
	"Gemfile.lock",
	"poetry.lock",
	"composer.lock",
	"Pipfile.lock",
	"requirements.txt",
}

func (n *LockfileNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	for _, name := range lockfileNames {
		if strings.HasSuffix(path, "/"+name) || path == name {
			return true
		}
	}
	return false
}

func (n *LockfileNormalizer) Description() string {
	return "lockfile"
}

// BuildPathNormalizer filters files in build-specific paths.
type BuildPathNormalizer struct{}

var buildPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/tmp/[^/]+/`),
	regexp.MustCompile(`/var/folders/[^/]+/`),
	regexp.MustCompile(`bazel-out/[^/]+/`),
	regexp.MustCompile(`/build/[^/]+/`),
	regexp.MustCompile(`/_build/`),
	regexp.MustCompile(`/target/debug/`),
	regexp.MustCompile(`/target/release/`),
	regexp.MustCompile(`/node_modules/\.cache/`),
	regexp.MustCompile(`/__pycache__/`),
}

func (n *BuildPathNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	for _, pattern := range buildPathPatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func (n *BuildPathNormalizer) Description() string {
	return "build path"
}

// SourceMapNormalizer filters source map differences.
type SourceMapNormalizer struct{}

func (n *SourceMapNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	return strings.HasSuffix(path, ".map") ||
		strings.HasSuffix(path, ".js.map") ||
		strings.HasSuffix(path, ".css.map")
}

func (n *SourceMapNormalizer) Description() string {
	return "source map"
}

// PycacheNormalizer filters Python cache files.
type PycacheNormalizer struct{}

func (n *PycacheNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	return strings.Contains(path, "__pycache__") ||
		strings.HasSuffix(path, ".pyc") ||
		strings.HasSuffix(path, ".pyo")
}

func (n *PycacheNormalizer) Description() string {
	return "Python cache"
}

// VersionStringNormalizer filters files with expected version string changes.
type VersionStringNormalizer struct {
	OldVersion string
	NewVersion string
}

var versionFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`/package\.json$`),
	regexp.MustCompile(`/version\.txt$`),
	regexp.MustCompile(`/VERSION$`),
	regexp.MustCompile(`/setup\.py$`),
	regexp.MustCompile(`/Cargo\.toml$`),
	regexp.MustCompile(`/pyproject\.toml$`),
}

func (n *VersionStringNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	if n.OldVersion == "" || n.NewVersion == "" {
		return false
	}
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	for _, pattern := range versionFilePatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func (n *VersionStringNormalizer) Description() string {
	return "version string"
}

// getFilePath extracts the file path from either resource.
func getFilePath(old, new *sgpb.Resource) string {
	if old != nil && old.GetFileInfo() != nil {
		return old.GetFileInfo().GetPath()
	}
	if new != nil && new.GetFileInfo() != nil {
		return new.GetFileInfo().GetPath()
	}
	return ""
}

// Path normalization for comparing executables across builds.
// These patterns strip random components from paths.
var pathNormalizationPatterns = []*regexp.Regexp{
	// Go build paths: /tmp/go-build1234567890/ -> /tmp/go-build*/
	regexp.MustCompile(`/tmp/go-build[0-9]+/`),
	// Generic temp dirs: /tmp/tmp1234abcd/ -> /tmp/tmp*/
	regexp.MustCompile(`/tmp/tmp[a-zA-Z0-9_]+/`),
	// mmdebstrap temp dirs
	regexp.MustCompile(`/tmp/mmdebstrap\.[a-zA-Z0-9]+`),
	// Build dirs with random suffix: /build/foo-AbCd12/ -> /build/foo-*/
	regexp.MustCompile(`(/build/[^/]+-)[a-zA-Z0-9]{6}/`),
	// Equivs cache dirs
	regexp.MustCompile(`/cache/equivs\.[a-zA-Z0-9]+/`),
	// Bazel output paths
	regexp.MustCompile(`bazel-out/[^/]+/`),
	// npm cache paths
	regexp.MustCompile(`/\.npm/_cacache/[^/]+/`),
	// Python build paths
	regexp.MustCompile(`/build/temp\.[^/]+/`),
}

// NormalizePath normalizes a path by stripping random build components.
func NormalizePath(path string) string {
	result := path
	for _, pattern := range pathNormalizationPatterns {
		result = pattern.ReplaceAllString(result, normalizeReplacement(pattern))
	}
	return result
}

func normalizeReplacement(pattern *regexp.Regexp) string {
	// Create a replacement that preserves the structure but uses * for random parts
	s := pattern.String()
	// Simple heuristic: replace character class patterns with *
	s = regexp.MustCompile(`\[[^\]]+\]\+?`).ReplaceAllString(s, "*")
	s = regexp.MustCompile(`\([^)]+\)`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\\.", ".")
	s = strings.ReplaceAll(s, "\\-", "-")
	return s
}

// CustomNormalizer allows users to define custom normalization patterns.
type CustomNormalizer struct {
	Patterns []*regexp.Regexp
	Desc     string
}

func (n *CustomNormalizer) ShouldNormalize(old, new *sgpb.Resource) bool {
	path := getFilePath(old, new)
	if path == "" {
		return false
	}
	for _, pattern := range n.Patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func (n *CustomNormalizer) Description() string {
	return n.Desc
}
