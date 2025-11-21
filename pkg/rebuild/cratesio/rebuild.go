// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pkg/errors"
)

// GetVersions returns the versions to be processed, most recent to least recent.
func GetVersions(ctx context.Context, pkg string, mux rebuild.RegistryMux) (versions []string, err error) {
	p, err := mux.CratesIO.Crate(ctx, pkg)
	if err != nil {
		return nil, err
	}
	var vs []reg.Version
	for _, v := range p.Versions {
		// Omit pre-release versions.
		// TODO: Support rebuilding pre-release versions.
		if strings.ContainsRune(v.Version, '-') {
			continue
		}
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool {
		return vs[i].Created.After(vs[j].Created)
	})
	for _, v := range vs {
		versions = append(versions, v.Version)
	}
	return versions, nil
}

func ArtifactName(t rebuild.Target) string {
	return fmt.Sprintf("%s-%s.crate", t.Package, t.Version)
}

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

const (
	cargoToml     = "Cargo.toml"
	cargoTomlOrig = "Cargo.toml.orig"
	cargoVCSInfo  = ".cargo_vcs_info.json"
)

var (
	verdictDSStore         = errors.New(".DS_STORE file(s) found in upstream but not rebuild")
	verdictLineEndings     = errors.New("Excess CRLF line endings found in upstream")
	verdictCargoVersion    = errors.New("only cargo-generated files differ")
	verdictCargoVersionGit = errors.New("only cargo-generated files and git ref differ")
	verdictMismatchedFiles = errors.New("mismatched file(s) in upstream and rebuild")
	verdictUpstreamOnly    = errors.New("file(s) found in upstream but not rebuild")
	verdictRebuildOnly     = errors.New("file(s) found in rebuild but not upstream")
	verdictContentDiff     = errors.New("content differences found")
)

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return true
}

func (r Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	return mux.CratesIO.Artifact(ctx, t.Package, t.Version)
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	crate, err := mux.CratesIO.Version(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching crate")
	}
	return crate.DownloadURL, nil
}
