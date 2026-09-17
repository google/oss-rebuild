// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// ExtractRequirements extracts requirements from build files in the specified directory.
func ExtractRequirements(ctx context.Context, tree *object.Tree, searchDir string) ([]string, error) {
	var reqs []string
	// Account for "" as base dir in the provided tree
	if searchDir == "" {
		searchDir = "."
	}
	configTypes := []struct {
		filename string
		extract  func(context.Context, *object.File) ([]string, error)
	}{
		{"pyproject.toml", extractPyProjectRequirements},
		{"setup.cfg", extractSetupCfgRequirements},
		{"setup.py", extractSetupPyRequirements},
	}
	for _, h := range configTypes {
		f, err := tree.File(filepath.Join(searchDir, h.filename))
		if err == object.ErrFileNotFound {
			continue
		} else if err != nil {
			return nil, errors.Wrapf(err, "finding %s file", h.filename)
		}
		fReqs, err := h.extract(ctx, f)
		if err != nil {
			return nil, errors.Wrapf(err, "extracting %s requirements", h.filename)
		}
		reqs = append(reqs, fReqs...)
	}
	return reqs, nil
}

// buildFileVerifiers pairs each build file with its per-format verifier.
var buildFileVerifiers = []struct {
	filename string
	verify   func(context.Context, *object.File, string, string) (fileVerification, error)
}{
	{"pyproject.toml", verifyPyProjectFile},
	{"setup.cfg", verifySetupCfgFile},
	{"setup.py", verifySetupPyFile},
}

// verifyBuildFiles verifies the build files in dir, or every build file in the
// tree when dir is "", against name and version. Files that fail to parse are
// logged and skipped.
func verifyBuildFiles(ctx context.Context, tree *object.Tree, dir, name, version string) ([]fileVerification, error) {
	var verified []fileVerification
	for _, h := range buildFileVerifiers {
		var files []*object.File
		if dir == "" {
			tree.Files().ForEach(func(f *object.File) error {
				if filepath.Base(f.Name) == h.filename {
					files = append(files, f)
				}
				return nil
			})
		} else if f, err := tree.File(filepath.Join(dir, h.filename)); err == nil {
			files = []*object.File{f}
		} else if err != object.ErrFileNotFound {
			return nil, errors.Wrapf(err, "finding %s file", h.filename)
		}
		for _, f := range files {
			v, err := h.verify(ctx, f, name, version)
			if err != nil {
				log.Printf("Failed to verify %s file: %v", h.filename, err)
				continue
			}
			verified = append(verified, v)
		}
	}
	return verified, nil
}

// DiscoverBuildDir searches for the best directory for requirement extraction.
// Returns the directory path relative to the tree root, with "" representing root.
func DiscoverBuildDir(ctx context.Context, tree *object.Tree, name, version, hintDir string) (string, error) {
	verified, err := verifyBuildFiles(ctx, tree, hintDir, name, version)
	if err != nil {
		return "", err
	}
	if len(verified) == 0 {
		return "", errors.New("no verified build files found for requirement extraction")
	}
	return rebuild.DirOf(sortVerifications(verified)[0].foundF.Name), nil
}
