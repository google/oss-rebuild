// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

func ExtractAllRequirements(ctx context.Context, tree *object.Tree, name, version, hintDir string) ([]string, string, error) {
	log.Println("Extracting any extra requirements from found build file types (pyproject.toml)")
	var reqs []string
	var foundFiles []foundFile

	foundPyprojFiles, err := findRecursively("pyproject.toml", tree, hintDir)
	if err != nil {
		log.Printf("Failed to find pyproject.toml files: %v", err)
	} else {
		foundFiles = append(foundFiles, foundPyprojFiles...)
	}

	// TODO setup.py

	foundSetupCfgFiles, err := findRecursively("setup.cfg", tree, hintDir)
	if err != nil {
		log.Printf("Failed to find setup.cfg files: %v", err)
	} else {
		foundFiles = append(foundFiles, foundSetupCfgFiles...)
	}

	if len(foundFiles) == 0 {
		return nil, "", errors.New("no supported build files found for requirement extraction")
	}

	var verifiedFiles []fileVerification

	for _, foundFile := range foundFiles {
		switch foundFile.filetype {
		case "pyproject.toml":
			verification, err := verifyPyProjectFile(ctx, foundFile, name, version)
			if err != nil {
				log.Printf("Failed to verify pyproject.toml file: %v", err)
				continue
			}
			verifiedFiles = append(verifiedFiles, verification)
		// TODO case setup.py
		case "setup.cfg":
			verification, err := verifySetupCfgFile(ctx, foundFile, name, version)
			if err != nil {
				log.Printf("Failed to verify setup.cfg file: %v", err)
				continue
			}
			verifiedFiles = append(verifiedFiles, verification)
		default:
			log.Printf("Unsupported file type for verification: %s", foundFile.filetype)
		}
	}

	if len(verifiedFiles) == 0 {
		return nil, "", errors.New("no verified build files found for requirement extraction")
	}

	sortedVerification := sortVerifications(verifiedFiles)

	bestFile := sortedVerification[0]
	dir := bestFile.foundF.path

	posFiles := []foundFile{bestFile.foundF}
	for _, f := range foundFiles {
		if f.path == dir && f.name != bestFile.foundF.name {
			posFiles = append(posFiles, f)
		}
	}

	for _, f := range posFiles {
		switch f.filetype {
		case "pyproject.toml":
			pyprojReqs, err := extractPyProjectRequirements(ctx, f.object)
			if err != nil {
				return nil, "", errors.Wrap(err, "Failed to extract pyproject.toml requirements")
			}

			reqs = append(reqs, pyprojReqs...)
		// TODO case setup.py
		case "setup.cfg":
			setupCfgReqs, err := extractSetupCfgRequirements(ctx, f.object)
			if err != nil {
				return nil, "", errors.Wrap(err, "Failed to extract pyproject.toml requirements")
			}

			reqs = append(reqs, setupCfgReqs...)
		default:
			log.Printf("Unsupported file type for requirement extraction: %s", f.filetype)
		}
	}

	// Account for "." as base dir
	if dir == "." {
		dir = ""
	}

	return reqs, dir, nil
}
