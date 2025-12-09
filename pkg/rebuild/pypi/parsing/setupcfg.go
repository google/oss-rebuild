// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/ini"
	"github.com/pkg/errors"
)

func splitRequiresList(value string) []string {
	// cfg specification in this doc: https://setuptools.pypa.io/en/latest/userguide/declarative_config.html
	// setup_requires may be list-semi (list separated by a semi-colon) or dangling list (newline seperated)
	lines := strings.Split(value, "\n")
	if len(lines) == 1 {
		// Try a semi-colon as a separator if no newlines or commas are found
		lines = strings.Split(value, ";")
	}

	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func verifySetupCfgFile(ctx context.Context, found foundFile, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = found
	f := found.object

	cfgContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to read setup.py")
	}

	cfgReader := strings.NewReader(cfgContents)

	cfg, err := ini.Parse(cfgReader)
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to parse setup.cfg")
	}

	foundName, fn := cfg.GetValue("metadata", "name")
	foundVersion, fv := cfg.GetValue("metadata", "version")

	if found.path == "." {
		verificationResult.main = true
	}

	if fn {
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName))
		verificationResult.levDistance = editDist

		if editDist == 0 {
			verificationResult.nameMatch = true
		}

		if fv && version == foundVersion {
			verificationResult.versionMatch = true
		}
	}

	return verificationResult, nil
}

func extractSetupCfgRequirements(ctx context.Context, f *object.File) ([]string, error) {
	log.Println("Looking for additional reqs in setup.cfg")
	cfgContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read setup.cfg")
	}

	cfgReader := strings.NewReader(cfgContents)

	cfg, err := ini.Parse(cfgReader)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to parse setup.cfg")
	}

	setupRequires, _ := cfg.GetValue("options", "setup_requires")
	setupRequiresList := splitRequiresList(setupRequires)

	log.Println("Added these reqs from setup.cfg: " + strings.Join(setupRequiresList, ", "))
	return setupRequiresList, nil
}
