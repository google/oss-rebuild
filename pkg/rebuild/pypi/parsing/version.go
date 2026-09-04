// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// FindDeclaredVersion returns the version a build file declares for pkg and
// the directory (per filepath.Dir, "." at the root) holding that file. dir is
// searched first, "" meaning the whole tree, and a miss there falls back to
// the whole tree, so the package is found wherever it has lived. Both are ""
// when no build file names pkg with a version.
func FindDeclaredVersion(ctx context.Context, tree *object.Tree, dir, pkg string) (version, foundDir string) {
	for _, d := range []string{dir, ""} {
		var verified []fileVerification
		for _, h := range buildFileVerifiers {
			files, err := findRecursively(h.filename, tree, d)
			if err != nil {
				continue
			}
			for _, f := range files {
				if v, err := h.verify(ctx, f, pkg, ""); err == nil && v.nameMatch && v.foundVersion != "" {
					verified = append(verified, v)
				}
			}
		}
		if len(verified) > 0 {
			best := sortVerifications(verified)[0]
			return best.foundVersion, filepath.Dir(best.foundF.Name)
		}
		if d == "" {
			break
		}
	}
	return "", ""
}
