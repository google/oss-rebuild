// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"context"
	"log"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

func (Rebuilder) Rebuild(ctx context.Context, t rebuild.Target, inst rebuild.Instructions, projectfs billy.Filesystem) error {
	if _, err := rebuild.ExecuteScript(ctx, projectfs.Root(), inst.Source); err != nil {
		return errors.Wrap(err, "fetching source")
	}
	if _, err := rebuild.ExecuteScript(ctx, projectfs.Root(), inst.Deps); err != nil {
		return errors.Wrap(err, "configuring build deps")
	}
	if _, err := rebuild.ExecuteScript(ctx, projectfs.Root(), inst.Build); err != nil {
		return errors.Wrap(err, "executing build")
	}
	return nil
}

var (
	verdictDSStore         = errors.New(".DS_STORE file(s) found in upstream but not rebuild")
	verdictLineEndings     = errors.New("Excess CRLF line endings found in upstream")
	verdictMismatchedFiles = errors.New("mismatched file(s) in upstream and rebuild")
	verdictUpstreamOnly    = errors.New("file(s) found in upstream but not rebuild")
	verdictRebuildOnly     = errors.New("file(s) found in rebuild but not upstream")
	verdictWheelDiff       = errors.New("wheel metadata mismatch")
	verdictContentDiff     = errors.New("content differences found")
)

func compareTwoFiles(csRB, csUP *archive.ContentSummary) (verdict error, err error) {
	upOnly, diffs, rbOnly := csUP.Diff(csRB)
	log.Println(upOnly, diffs, rbOnly)
	var foundDSStore bool
	for _, f := range upOnly {
		if strings.HasSuffix(f, "/.DS_STORE") {
			foundDSStore = true
		}
	}
	onlyMetadataDiffs := len(upOnly) == 0 && len(rbOnly) == 0 && len(diffs) > 0
	for _, f := range diffs {
		onlyMetadataDiffs = onlyMetadataDiffs && strings.Contains(f, ".dist-info/")
	}
	switch {
	case foundDSStore:
		return verdictDSStore, nil
	case csUP.CRLFCount > csRB.CRLFCount:
		return verdictLineEndings, nil
	case len(upOnly) > 0 && len(rbOnly) > 0:
		return verdictMismatchedFiles, nil
	case len(upOnly) > 0:
		return verdictUpstreamOnly, nil
	case len(rbOnly) > 0:
		return verdictRebuildOnly, nil
	case onlyMetadataDiffs:
		return verdictWheelDiff, nil
	case len(diffs) > 0:
		return verdictContentDiff, nil
	default:
		return nil, nil
	}
}

func (Rebuilder) Compare(ctx context.Context, t rebuild.Target, rb, up rebuild.Asset, assets rebuild.AssetStore, _ rebuild.Instructions) (verdict error, err error) {
	csRB, csUP, err := rebuild.Summarize(ctx, t, rb, up, assets)
	if err != nil {
		return nil, errors.Wrapf(err, "summarizing assets")
	}
	verdict, err = compareTwoFiles(csRB, csUP)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to compare %v to %v", rb, up)
	}
	log.Printf("Verdict for %s: %v", rb.Target.Artifact, verdict)
	return verdict, nil
}

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return true
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching project failed")
	}
	for _, a := range release.Artifacts {
		if a.Filename == t.Artifact {
			return a.URL, nil
		}
	}
	return "", errors.New("artifact not found")
}
