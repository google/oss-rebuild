// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/semver"
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

var sourceWithVersionRegex = regexp.MustCompile(`^(?P<source>[^\s]+)\s+\(\s*(?P<version>[^\s]+)\s*\)$`)
var pkgAndVersionRegex = regexp.MustCompile(`^(?P<pkg>[^\s]+)\s*\(\s*=\s*(?P<version>[^\s]+)\s*\),?$`)
var quotesRegex = regexp.MustCompile(`^"(?P<contents>.+)"$`)

func inferDebootsnapSbuild(t rebuild.Target, mux rebuild.RegistryMux) (rebuild.Strategy, error) {
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	a, err := debian.ParseDebianArtifact(t.Artifact)
	if err != nil {
		return nil, err
	}
	// The buildinfo uses the *source* package name, and the entire version string (including binary-only upload components).
	// This is because the buildinfo is versioned per build, not per source package release.
	infoURL, info, err := mux.Debian.BuildInfo(context.Background(), component, name, a.Version.String(), a.Arch)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch buildinfo")
	}
	// TODO: Populate the checksum
	strat := DebootsnapSbuild{BuildInfo: FileWithChecksum{URL: infoURL, MD5: ""}}
	{ // Architecture
		arches := strings.Fields(info.Architecture)
		filteredArches := []string{}
		for _, arch := range arches {
			if arch == "all" {
				strat.BuildArchAll = true
			} else if arch != "source" {
				// Only collect architectures that aren't "all" or "source"
				filteredArches = append(filteredArches, arch)
			}
		}
		if len(filteredArches) > 1 {
			return nil, errors.New("more than one architecture in Architecture field")
		}
		if len(filteredArches) == 1 {
			strat.BuildArchAny = true
		}
		strat.BuildArch = info.BuildArchitecture
		// In debrebuild.pl it looks like "Host-Architecture" is expected, but that field doesn't exist in the spec.
		if info.HostArchitecture != "" {
			strat.HostArch = info.HostArchitecture
		} else {
			strat.HostArch = strat.BuildArch
		}
	}
	// Source name and version
	// In some cases the source field contains a version in the form: name (version)
	if matches := sourceWithVersionRegex.FindStringSubmatch(info.Source); matches != nil {
		strat.SrcPackage = matches[sourceWithVersionRegex.SubexpIndex("source")]
		strat.SrcVersion = matches[sourceWithVersionRegex.SubexpIndex("version")]
	} else {
		strat.SrcPackage = info.Source
		strat.SrcVersion = info.Version
	}
	if strat.SrcPackage == "" {
		return nil, errors.New("missing source package name")
	}
	if strat.SrcVersion == "" {
		return nil, errors.New("missing source package version")
	}
	if sv, err := debian.ParseVersion(strat.SrcVersion); err != nil {
		return nil, errors.Wrap(err, "failed to parse source package version")
	} else {
		strat.SrcVersionNoEpoch = sv.Epochless()
	}
	// Build path
	if info.BuildPath != "" {
		strat.BuildPath = path.Dir(info.BuildPath)
		strat.DscDir = path.Base(strat.BuildPath)
	}
	// Environment
	for _, envVar := range info.Environment {
		envVar = strings.TrimSpace(envVar)
		if envVar == "" {
			continue
		}
		name, val, ok := strings.Cut(envVar, "=")
		if !ok {
			return nil, fmt.Errorf("unexpected environment variable: '%s'", envVar)
		}
		// Remove any quotes from the env var
		if match := quotesRegex.FindStringSubmatch(val); match != nil {
			val = match[quotesRegex.SubexpIndex("contents")]
		}
		strat.Env = append(strat.Env, fmt.Sprintf("%s=%s", name, val))
	}
	// Build Deps
	for _, dep := range info.InstalledBuildDepends {
		if matches := pkgAndVersionRegex.FindStringSubmatch(dep); matches != nil {
			pkg := matches[pkgAndVersionRegex.SubexpIndex("pkg")]
			if pkg == "dpkg" {
				strat.DpkgVersion = matches[pkgAndVersionRegex.SubexpIndex("version")]
				break
			}
		} else {
			return nil, fmt.Errorf("unexpected installed build dependency: '%s'", dep)
		}
	}
	if strat.DpkgVersion != "" {
		if v, err := debian.ParseVersion(strat.DpkgVersion); err == nil {
			if v.Epoch == "" || v.Epoch == "0" {
				if semver.Cmp(v.Upstream, "1.22.13") < 0 {
					strat.ForceRulesRequiresRootNo = true
				}
			}
		}
	}
	strat.BinaryOnlyChanges = info.BinaryOnlyChanges

	return &strat, nil
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	if _, ok := hint.(*DebianPackage); ok {
		return inferDSC(ctx, t, mux)
	} else if _, ok := hint.(*DebianPackage); ok {
		return inferDebrebuild(t, hint)
	}
	return inferDebootsnapSbuild(t, mux)
}
