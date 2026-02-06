// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/object"
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
		// TODO setup.py
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

// DiscoverBuildDir searches for the best directory for requirement extraction.
// Returns the directory path relative to the tree root, with "" representing root.
func DiscoverBuildDir(ctx context.Context, tree *object.Tree, name, version, hintDir string) (string, error) {
	var verifiedFiles []fileVerification
	configTypes := []struct {
		filename string
		verify   func(context.Context, *object.File, string, string) (fileVerification, error)
	}{
		{"pyproject.toml", verifyPyProjectFile},
		{"setup.cfg", verifySetupCfgFile},
		// TODO setup.py
	}
	for _, h := range configTypes {
		files, err := findRecursively(h.filename, tree, hintDir)
		if err != nil {
			return "", errors.Wrapf(err, "finding %s files", h.filename)
		}
		for _, f := range files {
			verification, err := h.verify(ctx, f, name, version)
			if err != nil {
				log.Printf("Failed to verify %s file: %v", h.filename, err)
				continue
			}
			verifiedFiles = append(verifiedFiles, verification)
		}
	}
	if len(verifiedFiles) == 0 {
		return "", errors.New("no verified build files found for requirement extraction")
	}
	sortedVerification := sortVerifications(verifiedFiles)
	bestFile := sortedVerification[0]
	dir := filepath.Dir(bestFile.foundF.Name)
	// Account for "." as base dir
	if dir == "." {
		dir = ""
	}
	return dir, nil
}
