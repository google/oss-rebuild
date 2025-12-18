// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/debian"
	"github.com/google/oss-rebuild/pkg/registry/debian/control"
	"github.com/pkg/errors"
)

// InferRepo is not needed because debian uses source packages.
func (Rebuilder) InferRepo(_ context.Context, _ rebuild.Target, _ rebuild.RegistryMux) (string, error) {
	return "", nil
}

// CloneRepo is not needed because debian uses source packages.
func (Rebuilder) CloneRepo(_ context.Context, _ rebuild.Target, _ string, _ *gitx.RepositoryOptions) (rebuild.RepoConfig, error) {
	return rebuild.RepoConfig{}, nil
}

// Source packages are expected to end with .tar.gz in format 3.0 or .diff.gz in format 1.0:
// https://wiki.debian.org/Packaging/SourcePackage#The_definition_of_a_source_package
// In the wild, we've seen a few additional compression schemes used.
var origRegex = regexp.MustCompile(`\.orig\.tar\.(gz|xz|bz2)$`)
var debianRegex = regexp.MustCompile(`\.(debian\.tar|diff)\.(gz|xz|bz2)$`)
var nativeRegex = regexp.MustCompile(`\.tar\.(gz|xz|bz2)$`)

func inferDSC(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (rebuild.Strategy, error) {
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	p := DebianPackage{}
	var dsc *control.ControlFile
	p.DSC.URL, dsc, err = mux.Debian.DSC(ctx, component, name, t.Version)
	if err != nil {
		return nil, err
	}
	for stanza := range dsc.Stanzas {
		for field, values := range dsc.Stanzas[stanza].Fields {
			switch field {
			case "Files":
				for _, value := range values {
					elems := strings.Split(strings.TrimSpace(value), " ")
					if len(elems) != 3 {
						return nil, errors.Errorf("unexpected dsc File element: %s", value)
					}
					md5 := elems[0]
					f := elems[2]
					if origRegex.FindStringIndex(f) != nil {
						p.Orig.URL = debian.PoolURL(component, name, f)
						p.Orig.MD5 = md5
					} else if debianRegex.FindStringIndex(f) != nil {
						p.Debian.URL = debian.PoolURL(component, name, f)
						p.Debian.MD5 = md5
					} else if nativeRegex.FindStringIndex(f) != nil {
						if p.Native.URL != "" {
							return nil, errors.Errorf("multiple matches for native source: %s, %s", p.Native.URL, f)
						}
						p.Native.URL = debian.PoolURL(component, name, f)
						p.Native.MD5 = md5
					}
				}
			case "Build-Depends", "Build-Depends-Indep":
				deps := strings.Split(strings.TrimSpace(values[0]), ",")
				for i, dep := range deps {
					dep = strings.TrimSpace(dep)
					if strings.Contains(dep, " ") {
						deps[i] = strings.TrimSpace(strings.Split(dep, " ")[0])
					}
				}
				p.Requirements = append(p.Requirements, deps...)
			}
		}
	}
	if (p.Orig.URL == "" || p.Debian.URL == "") && (p.Native.URL == "") {
		return nil, errors.Errorf("failed to find source files in the .dsc file: %s", p.DSC.URL)
	}
	return &p, nil
}

func inferDebrebuild(t rebuild.Target, hint rebuild.Strategy) (rebuild.Strategy, error) {
	_, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	a, err := debian.ParseDebianArtifact(t.Artifact)
	if err != nil {
		return nil, err
	}
	// The buildinfo uses the *source* package name, and the entire version string (including binary-only upload components).
	// This is because the buildinfo is versioned per build, not per source package release.
	infoURL := debian.BuildInfoURL(name, a.Version.String(), a.Arch)
	// TODO: Populate the checksum
	strat := Debrebuild{BuildInfo: FileWithChecksum{URL: infoURL, MD5: ""}}
	if s, ok := hint.(*Debrebuild); ok {
		if s.UseNoCheck {
			strat.UseNoCheck = true
		}
	}
	return &strat, nil
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	if _, ok := hint.(*DebianPackage); ok {
		return inferDSC(ctx, t, mux)
	}
	return inferDebrebuild(t, hint)
}
