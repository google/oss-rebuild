// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// GetVersions returns the versions to be processed, most recent to least recent.
func GetVersions(ctx context.Context, pkg string, mux rebuild.RegistryMux) (versions []string, err error) {
	p, err := mux.NPM.Package(ctx, pkg)
	if err != nil {
		return nil, err
	}
	for v := range p.Versions {
		// Omit pre-release versions.
		// TODO: Make this configurable.
		if strings.ContainsRune(v, '-') {
			continue
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return p.UploadTimes[versions[i]].After(p.UploadTimes[versions[j]])
	})
	return versions, err
}

func sanitize(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "@", ""), "/", "-")
}

func ArtifactName(t rebuild.Target) string {
	return fmt.Sprintf("%s-%s.tgz", sanitize(t.Package), t.Version)
}

func makeUsrLocalCleanup() func() {
	existing := make(map[string]bool)
	basepath := "/usr/local"
	basefs := osfs.New(basepath)
	util.Walk(basefs, ".", func(path string, info fs.FileInfo, err error) error {
		existing[filepath.Join(basepath, path)] = true
		return nil
	})
	return func() {
		log.Println("cleaning up Node install")
		util.Walk(basefs, ".", func(path string, info fs.FileInfo, err error) error {
			fullpath := filepath.Join(basepath, path)
			if !existing[fullpath] {
				os.RemoveAll(fullpath)
			}
			return nil
		})
	}
}

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

var (
	verdictMissingDist        = errors.New("dist/ file(s) found in upstream but not rebuild")
	verdictDSStore            = errors.New(".DS_STORE file(s) found in upstream but not rebuild")
	verdictLineEndings        = errors.New("Excess CRLF line endings found in upstream")
	verdictMismatchedFiles    = errors.New("mismatched file(s) in upstream and rebuild")
	verdictUpstreamOnly       = errors.New("file(s) found in upstream but not rebuild")
	verdictHiddenUpstreamOnly = errors.New("hidden file(s) found in upstream but not rebuild")
	verdictRebuildOnly        = errors.New("file(s) found in rebuild but not upstream")
	verdictPackageJSONDiff    = errors.New("package.json differences found")
	verdictContentDiff        = errors.New("content differences found")
)

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return true
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	vmeta, err := mux.NPM.Version(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	return vmeta.Dist.URL, nil
}
