// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"path/filepath"
	re "regexp"
	"strings"

	"github.com/google/oss-rebuild/pkg/ini"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

var dateSuffixPat = re.MustCompile(`\d{8}`)

func isTruthySetupCfgBool(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// NeedsSetupCfgSanitize reports whether setup.cfg in searchDir sets [egg_info]
// tag_build or tag_date to a value that conflicts with the release version.
// tag_build is matched as a case-insensitive substring after trimming leading
// "._-" characters, and tag_date is considered satisfied when version contains
// any eight-digit date run (since a dated release cannot be reproduced by
// either clearing the date or stamping today's date).
func NeedsSetupCfgSanitize(ctx context.Context, tree *git2.Tree, searchDir, version string) (bool, error) {
	if searchDir == "" {
		searchDir = "."
	}
	f, err := tree.File(filepath.Join(searchDir, "setup.cfg"))
	if errors.Is(err, git2.ErrFileNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "finding setup.cfg file")
	}
	cfgContents, err := f.Contents()
	if err != nil {
		return false, errors.Wrap(err, "reading setup.cfg")
	}
	cfg, err := ini.Parse(strings.NewReader(cfgContents))
	if err != nil {
		// NOTE: A setup.cfg our parser rejects is a rejected candidate, not an
		// infrastructure failure. The build decides whether setuptools accepts it.
		return false, nil
	}
	if tagBuild, ok := cfg.GetValue("egg_info", "tag_build"); ok {
		tag := strings.TrimLeft(strings.TrimSpace(tagBuild), "._-")
		if tag != "" && !strings.Contains(strings.ToLower(version), strings.ToLower(tag)) {
			return true, nil
		}
	}
	if tagDate, ok := cfg.GetValue("egg_info", "tag_date"); ok {
		if isTruthySetupCfgBool(tagDate) && !dateSuffixPat.MatchString(version) {
			return true, nil
		}
	}
	return false, nil
}

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

func verifySetupCfgFile(ctx context.Context, f *git2.File, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = f

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
	// 'attr:' and 'file:' directives are dynamically resolved so provide no version here.
	if fv && (strings.HasPrefix(foundVersion, "attr:") || strings.HasPrefix(foundVersion, "file:")) {
		foundVersion, fv = "", false
	}

	if filepath.Dir(f.Name) == "." {
		verificationResult.main = true
	}

	if fn {
		editDist := minEditDistance(pypireg.NormalizedName(name), pypireg.NormalizedName(foundName))
		verificationResult.levDistance = editDist

		if editDist == 0 {
			verificationResult.nameMatch = true
		}

		if fv {
			verificationResult.foundVersion = foundVersion
		}
		if fv && SameVersion(foundVersion, version) {
			verificationResult.versionMatch = true
		}
	}

	return verificationResult, nil
}

func extractSetupCfgRequirements(ctx context.Context, f *git2.File) ([]string, error) {
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
