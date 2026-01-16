// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"compress/gzip"
	"context"
	"io"
	"log"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/debian"
	"github.com/google/oss-rebuild/pkg/registry/debian/control"
	"github.com/pkg/errors"
)

// refExistsInRepo checks whether a git ref (commit hash or tag) exists in the repository.
func refExistsInRepo(repo *git.Repository, ref string) bool {
	if repo == nil {
		return false
	}
	// Try to resolve as a commit hash
	hash := plumbing.NewHash(ref)
	if _, err := repo.CommitObject(hash); err == nil {
		return true
	}
	// Try to resolve as a tag reference
	if _, err := repo.Tag(ref); err == nil {
		return true
	}
	// Try to resolve as a branch reference
	if _, err := repo.Reference(plumbing.ReferenceName("refs/heads/"+ref), true); err == nil {
		return true
	}
	return false
}

// InferRepo infers the upstream git repository for orig tarballs.
// For standard Debian packages, returns empty string.
func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// Only infer repo for orig tarballs
	if origRegex.FindStringIndex(t.Artifact) == nil {
		return "", nil
	}
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return "", err
	}
	// Try to get repo from DSC file
	_, dsc, err := mux.Debian.DSC(ctx, component, name, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "no repo found")
	}
	// TODO: Try to use the debian/watch file
	// Look for Vcs-Git or Homepage fields
	for stanza := range dsc.Stanzas {
		for field, val := range dsc.Stanzas[stanza].Fields {
			switch field {
			case "Vcs-Git", "Homepage":
				repoVal, err := val.AsSimple()
				if err != nil {
					continue
				}
				if repo, err := uri.CanonicalizeRepoURI(repoVal); err == nil {
					return repo, nil
				}
			}
		}
	}
	return "", nil
}

// CloneRepo clones the upstream git repository for orig tarballs.
// For standard Debian packages, returns empty config.
func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, opts *gitx.RepositoryOptions) (r rebuild.RepoConfig, err error) {
	// Only clone repo for orig tarballs
	if origRegex.FindStringIndex(t.Artifact) == nil || repoURI == "" {
		return rebuild.RepoConfig{}, nil
	}
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, opts.Storer, opts.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	if err != nil {
		return rebuild.RepoConfig{}, errors.Wrap(err, "cloning repository")
	}
	return r, nil
}

// Source packages are expected to end with .tar.gz in format 3.0 or .diff.gz in format 1.0:
// https://wiki.debian.org/Packaging/SourcePackage#The_definition_of_a_source_package
var (
	origRegex   = regexp.MustCompile(`\.orig\.tar\.(gz|xz|bz2)$`)
	debianRegex = regexp.MustCompile(`\.(debian\.tar|diff)\.(gz|xz|bz2)$`)
	nativeRegex = regexp.MustCompile(`\.tar\.(gz|xz|bz2)$`)
)

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
	// TODO: Move this Control parsing into the control package as a specific DSC type (similar to BuildInfo)
	for stanza := range dsc.Stanzas {
		for field, val := range dsc.Stanzas[stanza].Fields {
			switch field {
			case "Files":
				for _, line := range val.AsLines() {
					elems := strings.Fields(line)
					if len(elems) != 3 {
						return nil, errors.Errorf("unexpected dsc File element: %s", line)
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
				deps := val.AsList()
				for i, dep := range deps {
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
	v, err := debian.ParseVersion(t.Version)
	if err != nil {
		return nil, err
	}
	a, err := debian.ParseDebianArtifact(t.Artifact)
	if err != nil {
		return nil, err
	}
	// The buildinfo uses the *source* package name, and the entire version string (including binary-only upload components).
	// This is because the buildinfo is versioned per build, not per source package release.
	// We use the target version rather than a.Version to ensure epoch is present.
	infoURL := debian.BuildInfoURL(name, v, a.Arch)
	// TODO: Populate the checksum
	strat := Debrebuild{BuildInfo: FileWithChecksum{URL: infoURL, MD5: ""}}
	if s, ok := hint.(*Debrebuild); ok {
		if s.UseNoCheck {
			strat.UseNoCheck = true
		}
	}
	return &strat, nil
}

func inferUpstreamSource(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig) (rebuild.Strategy, error) {
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	// Parse version to get upstream version
	version, err := debian.ParseVersion(t.Version)
	if err != nil {
		return nil, errors.Wrap(err, "parsing version")
	}
	// Infer compression and prefix from the artifact name
	// Expected format: <package>_<version>.orig.tar.<compression>
	var compression string
	if strings.HasSuffix(t.Artifact, ".tar.gz") {
		compression = "gz"
	} else if strings.HasSuffix(t.Artifact, ".tar.bz2") {
		// compression = "bz2" not yet supported
		return nil, errors.New("unsupported archive format: bz2")
	} else if strings.HasSuffix(t.Artifact, ".tar.xz") {
		// compression = "xz" not yet supported
		return nil, errors.New("unsupported archive format: xz")
	} else {
		return nil, errors.Errorf("unknown archive format: %s", t.Artifact)
	}
	// TODO: Although standard, we should detect whatever the prefix format used
	prefix := name + "-" + version.Upstream + "/"
	// Try to get existing orig tarball and extract commit ID
	var location rebuild.Location
	location.Repo = rcfg.URI
	var extractedCommitID string
	origReader, err := mux.Debian.Artifact(ctx, component, name, t.Artifact)
	if err == nil {
		defer origReader.Close()
		var decompressedReader io.Reader
		switch compression {
		case "gz":
			decompressedReader, err = gzip.NewReader(origReader)
			if err != nil {
				return nil, err
			}
		}
		commitID, err := ExtractTarCommitID(decompressedReader)
		if err == nil && commitID != "" {
			extractedCommitID = commitID
			// Validate that the extracted commit ID exists in the inferred repository
			if refExistsInRepo(rcfg.Repository, commitID) {
				location.Ref = commitID
				log.Printf("Using extracted commit ID from tarball: %s", commitID)
			} else {
				log.Printf("Extracted commit ID %s not found in repository %s, falling back to tag inference", commitID, rcfg.URI)
			}
		}
	}
	// If we don't have a validated ref, try to resolve from version using tag matching
	if location.Ref == "" {
		tagRef, err := rebuild.FindTagMatch(t.Package, version.Upstream, rcfg.Repository)
		if err != nil {
			return nil, err
		}
		if tagRef == "" {
			if extractedCommitID != "" {
				return nil, errors.Errorf("extracted commit ID %s not found in repo and no matching tag found for version %s", extractedCommitID, version.Upstream)
			}
			return nil, errors.Errorf("no matching tag found for version %s", version.Upstream)
		}
		location.Ref = tagRef
		log.Printf("Using tag-inferred ref: %s", tagRef)
	}
	return &UpstreamSourceArchive{
		Location:       location,
		Compression:    compression,
		Prefix:         prefix,
		OutputFilename: t.Artifact,
	}, nil
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	// If the artifact is an orig tarball, infer UpstreamSourceArchive strategy
	if origRegex.FindStringIndex(t.Artifact) != nil {
		return inferUpstreamSource(ctx, t, mux, rcfg)
	}
	if _, ok := hint.(*DebianPackage); ok {
		return inferDSC(ctx, t, mux)
	}
	return inferDebrebuild(t, hint)
}
