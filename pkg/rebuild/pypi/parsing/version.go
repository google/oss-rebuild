// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"cmp"
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/versionx"
)

// FindDeclaredVersion returns the version a build file declares for pkg and
// the directory (per filepath.Dir, "." at the root) holding that file. dir is
// searched first, "" meaning the whole tree, and a miss there falls back to
// the whole tree, so the package is found wherever it has lived. Both are ""
// when no build file names pkg with a version.
func FindDeclaredVersion(ctx context.Context, tree *object.Tree, dir, pkg string) (version, foundDir string) {
	for _, d := range slices.Compact([]string{dir, ""}) {
		verified, err := verifyBuildFiles(ctx, tree, d, pkg, "")
		if err != nil {
			continue
		}
		verified = slices.DeleteFunc(verified, func(v fileVerification) bool { return !v.nameMatch || v.foundVersion == "" })
		if len(verified) > 0 {
			best := sortVerifications(verified)[0]
			return best.foundVersion, filepath.Dir(best.foundF.Name)
		}
	}
	return "", ""
}

// SameVersion reports whether a and b spell the same version. Spellings are
// compared under the approximate ordering, after rewriting the pre-release
// words PEP 440 accepts into the ones it canonicalizes to, so 1.0.0-beta.2 and
// 1.0.0b2 are the same.
func SameVersion(a, b string) bool {
	return versionx.ApproxCompare(pep440Spelling(a), pep440Spelling(b)) == 0
}

func pep440Spelling(v string) string {
	return wordRun.ReplaceAllStringFunc(v, func(w string) string {
		w = strings.ToLower(w)
		return cmp.Or(pep440Words[w], w)
	})
}

var (
	wordRun     = regexp.MustCompile(`[A-Za-z]+`)
	pep440Words = map[string]string{"alpha": "a", "beta": "b", "c": "rc", "pre": "rc", "preview": "rc"}
)
