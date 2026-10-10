// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/semver"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pelletier/go-toml/v2"
)

var stableRelease = regexp.MustCompile(`^1\.\d+(\.\d+)?$`)

var allowedToolchainKeys = map[string]bool{
	"channel":    true,
	"components": true,
	"targets":    true,
	"profile":    true,
}

// findPinnedStableToolchain searches dir and its ancestors for a repository
// toolchain file, returning any pinned 2- or 3-component stable channel and
// whether a toolchain file was found.
//
// TODO: Resolve nightly channels as well. Dated nightlies, including their
// x86_64-unknown-linux-musl host builds and channel manifests, are retained
// indefinitely under static.rust-lang.org/dist/<date>/, so a "nightly" pin
// could map to nightly-<publish date> (and "nightly-<date>" to itself) once
// the toolchain cache and rustup proxy carry nightly manifests. Until then
// these pins fall back to the inferred stable release.
func findPinnedStableToolchain(tree *object.Tree, dir string) (string, bool, error) {
	current := path.Clean(dir)
	if path.IsAbs(current) {
		return "", false, nil
	}
	for {
		for _, name := range []string{"rust-toolchain", "rust-toolchain.toml"} {
			file, err := tree.File(path.Join(current, name))
			if err == object.ErrFileNotFound {
				continue
			}
			if err != nil {
				return "", false, err
			}
			if file.Mode != filemode.Regular && file.Mode != filemode.Executable {
				return "", false, nil
			}
			contents, err := file.Contents()
			if err != nil {
				return "", false, err
			}
			channel, _, err := parseStableToolchain(contents)
			return channel, true, err
		}
		if current == "." {
			return "", false, nil
		}
		current = path.Dir(current)
	}
}

func parseStableToolchain(contents string) (string, bool, error) {
	contents = strings.TrimSpace(contents)
	if stableRelease.MatchString(contents) {
		return contents, true, nil
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(contents), &document); err != nil {
		return "", false, nil
	}
	if len(document) != 1 {
		return "", false, nil
	}
	toolchain, ok := document["toolchain"].(map[string]any)
	if !ok {
		return "", false, nil
	}
	for k := range toolchain {
		if !allowedToolchainKeys[k] {
			return "", false, nil
		}
	}
	channel, ok := toolchain["channel"].(string)
	if !ok || !stableRelease.MatchString(channel) {
		return "", false, nil
	}
	return channel, true, nil
}

// resolvePinnedToolchain resolves a 2- or 3-component stable channel (e.g.
// "1.70" or "1.84.1") to a MUSL-supported release version published on or
// before latestAtPublish.
func resolvePinnedToolchain(channel, latestAtPublish string) (string, bool) {
	if strings.Count(channel, ".") == 1 {
		resolved := ""
		for patch := 0; ; patch++ {
			candidate := fmt.Sprintf("%s.%d", channel, patch)
			if semver.Cmp(candidate, latestAtPublish) > 0 {
				break
			}
			hasMUSL, err := reg.HasMUSLBuild(candidate)
			if err != nil {
				break
			}
			if hasMUSL {
				resolved = candidate
			}
		}
		return resolved, resolved != ""
	}
	if semver.Cmp(channel, latestAtPublish) > 0 {
		return "", false
	}
	hasMUSL, err := reg.HasMUSLBuild(channel)
	if err != nil || !hasMUSL {
		return "", false
	}
	return channel, true
}
