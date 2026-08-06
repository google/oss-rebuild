// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"math"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

func verifySetupPyFile(ctx context.Context, f *object.File, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = f
	setupPyContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "reading setup.py")
	}
	if filepath.Dir(f.Name) == "." {
		verificationResult.main = true
	}
	setupCalls, err := setupPyArgs([]byte(setupPyContents))
	if err != nil {
		return verificationResult, errors.Wrap(err, "parsing setup.py")
	}
	// A file may hold several setup() calls, typically forked on interpreter
	// version. Score it on whichever one names the closest package.
	closest := math.MaxInt
	for _, args := range setupCalls {
		foundName, ok := args["name"]
		if !ok || foundName.kind != pyString {
			continue
		}
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName.str))
		if editDist >= closest {
			continue
		}
		closest = editDist
		verificationResult.levDistance = editDist
		verificationResult.nameMatch = editDist == 0
		foundVersion, ok := args["version"]
		verificationResult.versionMatch = ok && foundVersion.kind == pyString && foundVersion.str == version
	}
	return verificationResult, nil
}

func extractSetupPyRequirements(ctx context.Context, f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in setup.py")
	setupPyContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "reading setup.py")
	}
	setupCalls, err := setupPyArgs([]byte(setupPyContents))
	if err != nil {
		return nil, errors.Wrap(err, "parsing setup.py")
	}
	for _, args := range setupCalls {
		switch setupRequires := args["setup_requires"]; setupRequires.kind {
		case pyString:
			reqs = append(reqs, setupRequires.str)
		case pyStringList:
			reqs = append(reqs, setupRequires.list...)
		}
	}
	log.Println("Added these reqs from setup.py: " + strings.Join(reqs, ", "))
	return reqs, nil
}
