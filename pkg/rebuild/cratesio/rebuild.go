// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
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

func (Rebuilder) Rebuild(ctx context.Context, t rebuild.Target, inst rebuild.Instructions, fs billy.Filesystem) error {
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Source); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Source")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Deps); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Deps")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Build); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Build")
	}
	return nil
}

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

func (Rebuilder) Compare(ctx context.Context, t rebuild.Target, rb, up rebuild.Asset, assets rebuild.AssetStore, inst rebuild.Instructions) (msg error, err error) {
	csRB, csUP, err := rebuild.Summarize(ctx, t, rb, up, assets)
	if err != nil {
		return nil, errors.Wrapf(err, "summarizing assets")
	}
	upOnly, diffs, rbOnly := csUP.Diff(csRB)
	var foundDSStore bool
	for _, f := range upOnly {
		if strings.HasSuffix(f, "/.DS_STORE") {
			foundDSStore = true
		}
	}
	prefix := strings.TrimSuffix(t.Artifact, ".crate")
	var cargoVersionDiff bool
	{
		metadataFiles := []string{path.Join(prefix, cargoToml), path.Join(prefix, cargoVCSInfo)}
		if orig := slices.Index(csUP.Files, path.Join(prefix, cargoTomlOrig)); orig == -1 {
			// If upstream has no orig file (i.e. from an older version of cargo),
			// check the Cargo.toml file against the rebuilt orig since this compares
			// the user-defined content.
			tomlHash := csUP.FileHashes[slices.Index(csUP.Files, path.Join(prefix, cargoToml))]
			origHash := csRB.FileHashes[slices.Index(csRB.Files, path.Join(prefix, cargoTomlOrig))]
			if tomlHash == origHash {
				metadataFiles = append(metadataFiles, path.Join(prefix, cargoTomlOrig))
			}
		}
		allDiffs := slices.Clone(rbOnly)
		allDiffs = append(allDiffs, upOnly...)
		allDiffs = append(allDiffs, diffs...)
		cargoVersionDiff = len(allDiffs) > 0
		for _, f := range allDiffs {
			if !slices.Contains(metadataFiles, f) {
				cargoVersionDiff = false
				break
			}
		}
	}
	var gitRefDiff bool
	{
		var upRef string
		{
			r, err := assets.Reader(ctx, up)
			if err != nil {
				return nil, errors.Wrapf(err, "reading upstream")
			}
			defer r.Close()
			f, err := getFileFromCrate(r, path.Join(prefix, cargoVCSInfo))
			if err != nil {
				log.Printf("failed to read VCS info from crate: %v", err)
			} else {
				var info reg.CargoVCSInfo
				if err := json.Unmarshal(f, &info); err != nil {
					log.Printf("failed to unmarshal VCS info from crate: %v", err)
				} else {
					upRef = info.GitInfo.SHA1
				}
			}
		}
		gitRefDiff = upRef != inst.Location.Ref
	}
	switch {
	case foundDSStore:
		return verdictDSStore, nil
	case csUP.CRLFCount > csRB.CRLFCount:
		return verdictLineEndings, nil
	case cargoVersionDiff && gitRefDiff:
		return verdictCargoVersionGit, nil
	case cargoVersionDiff:
		return verdictCargoVersion, nil
	case len(upOnly) > 0 && len(rbOnly) > 0:
		return verdictMismatchedFiles, nil
	case len(upOnly) > 0:
		return verdictUpstreamOnly, nil
	case len(rbOnly) > 0:
		return verdictRebuildOnly, nil
	case len(diffs) > 0:
		return verdictContentDiff, nil
	default:
		return nil, nil
	}
}

// RebuildMany executes rebuilds for each provided rebuild.Input returning their rebuild.Verdicts.
func RebuildMany(ctx context.Context, inputs []rebuild.Input, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	for i := range inputs {
		inputs[i].Target.Artifact = ArtifactName(inputs[i].Target)
	}
	return rebuild.RebuildMany(ctx, Rebuilder{}, inputs, mux)
}

// RebuildRemote executes the given target strategy on a remote builder.
func RebuildRemote(ctx context.Context, input rebuild.Input, id string, opts rebuild.RemoteOptions) error {
	opts.UseTimewarp = false
	return rebuild.RebuildRemote(ctx, input, id, opts)
}
