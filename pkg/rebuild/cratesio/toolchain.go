// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"path"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pelletier/go-toml/v2"
)

var stableRelease = regexp.MustCompile(`^1\.\d+\.\d+$`)

// findPinnedStableToolchain returns a fully-qualified stable channel when the
// effective repository toolchain file does not request additional setup.
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
			return parseStableToolchain(contents)
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
	if !ok || len(toolchain) != 1 {
		return "", false, nil
	}
	channel, ok := toolchain["channel"].(string)
	if !ok || !stableRelease.MatchString(channel) {
		return "", false, nil
	}
	return channel, true, nil
}
