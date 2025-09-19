// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

func getCargoTOML(tree *object.Tree, path string) (ct reg.CargoTOML, err error) {
	f, err := tree.File(path)
	if err != nil {
		return ct, err
	}
	p, err := f.Contents()
	if err != nil {
		return ct, err
	}
	return ct, toml.Unmarshal([]byte(p), &ct)
}

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	pmeta, err := mux.CratesIO.Crate(ctx, t.Package)
	if err != nil {
		return "", err
	}
	return uri.CanonicalizeRepoURI(pmeta.Repository)
}

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, ropt *gitx.RepositoryOptions) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, ropt.Storer, ropt.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
	// Do Cargo.toml search.
	head, _ := r.Repository.Head()
	c, _ := r.Repository.CommitObject(head.Hash())
	_, pkgPath, err := findCargoTOML(r.Repository, c, t.Package)
	if err != nil {
		log.Printf("Cargo.toml path heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, r.URI, err.Error())
		r.Dir = "."
		log.Println("Skipping ref map search")
		r.RefMap = make(map[string]string)
		err = nil
	} else {
		r.Dir = path.Dir(pkgPath)
		// Do version heuristic search.
		r.RefMap, err = cargoTOMLSearch(t.Package, pkgPath, r.Repository)
		if err != nil {
			log.Printf("Cargo.toml version heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, r.URI, err.Error())
		}
	}
	return r, err
}

func inferRefAndDir(t rebuild.Target, vmeta *reg.CrateVersion, crateBytes []byte, rcfg *rebuild.RepoConfig) (ref, dir string, err error) {
	// Determine git ref to rebuild.
	cargoTOMLGuess := rcfg.RefMap[t.Version]
	tagGuess, err := rebuild.FindTagMatch(t.Package, t.Version, rcfg.Repository)
	if err != nil {
		return "", "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	var cargoVCSGuess string
	topLevel := t.Package + "-" + vmeta.Version.Version
	vcsInfo, err := getFileFromCrate(bytes.NewReader(crateBytes), topLevel+"/.cargo_vcs_info.json")
	var info reg.CargoVCSInfo
	if errors.Is(err, fs.ErrNotExist) {
		log.Printf("No .cargo_vcs_info.json file found")
	} else if err != nil {
		return "", "", errors.Wrapf(err, "[INTERNAL] Failed to extract upstream .cargo_vcs_info.json")
	} else if err := json.Unmarshal(vcsInfo, &info); err != nil {
		return "", "", errors.Wrapf(err, "[INTERNAL] Failed to extract upstream .cargo_vcs_info.json")
	} else {
		cargoVCSGuess = info.GitInfo.SHA1
	}
	dir = rcfg.Dir
	var c *object.Commit
	switch {
	// Ensure the package config has the expected name and version.
	case cargoVCSGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(cargoVCSGuess))
		if err == nil {
			if newPath, err := findAndValidateCargoTOML(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry ref invalid: %v", err)
			} else {
				log.Printf("using registry ref: %s", cargoVCSGuess[:9])
				ref = cargoVCSGuess
				dir = filepath.Dir(newPath)
				return ref, dir, nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("cargo_vcs_info ref not found in repo")
		} else {
			return "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from cargo_vcs_info [repo=%s,ref=%s]", rcfg.URI, cargoVCSGuess)
		}
		log.Printf("ref heuristic cargo_vcs_info not found in repo")
		fallthrough
	case tagGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidateCargoTOML(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry heuristic tag invalid: %v", err)
			} else {
				log.Printf("using tag heuristic ref: %s", tagGuess[:9])
				ref = tagGuess
				dir = filepath.Dir(newPath)
				return ref, dir, nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("tag heuristic ref not found in repo")
		} else {
			return "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", rcfg.URI, tagGuess)
		}
		log.Printf("ref heuristic tag not found in repo")
		fallthrough
	case cargoTOMLGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(cargoTOMLGuess))
		if err == nil {
			if newPath, err := findAndValidateCargoTOML(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry heuristic git log invalid: %v", err)
			} else {
				log.Printf("using git log heuristic ref: %s", cargoTOMLGuess[:9])
				ref = cargoTOMLGuess
				dir = filepath.Dir(newPath)
				return ref, dir, nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("git log heuristic ref not found in repo")
		} else {
			return "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", rcfg.URI, cargoTOMLGuess)
		}
		log.Printf("ref heuristic git log not found in repo")
		fallthrough
	default:
		if cargoVCSGuess == "" && tagGuess == "" && cargoTOMLGuess == "" {
			return "", "", errors.Errorf("no git ref")
		}
		return "", "", errors.Errorf("no valid git ref")
	}
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	vmeta, err := mux.CratesIO.Version(ctx, name, version)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to fetch crate version")
	}
	r, err := mux.CratesIO.Artifact(ctx, name, version)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to fetch upstream crate")
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to read upstream crate")
	}
	var ref, dir string
	lh, ok := hint.(*rebuild.LocationHint)
	if hint != nil && !ok {
		return nil, errors.Errorf("unsupported hint type: %T", hint)
	}
	if lh != nil && lh.Ref != "" {
		ref = lh.Ref
		if lh.Dir != "" {
			dir = lh.Dir
		} else {
			dir = rcfg.Dir
		}
	} else {
		ref, dir, err = inferRefAndDir(t, vmeta, b, rcfg)
		if err != nil {
			return nil, err
		}
	}
	c, err := rcfg.Repository.CommitObject(plumbing.NewHash(ref))
	if err != nil {
		return nil, err
	}
	tree, _ := c.Tree()
	ct, err := getCargoTOML(tree, path.Join(dir, "Cargo.toml"))
	if err == object.ErrFileNotFound {
		return nil, errors.Errorf("Cargo.toml file not found [heuristic=%s]", rcfg.Dir)
	} else if _, ok := err.(*toml.DecodeError); ok {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to parse Cargo.toml")
	}
	if ct.Name != name {
		return nil, errors.Errorf("mismatched name [expected=%s,actual=%s,heuristic=%s]", name, ct.Name, rcfg.Dir)
	}
	if ct.Version() != version && ct.Version() != reg.WorkspaceVersion {
		return nil, errors.Errorf("mismatched version [expected=%s,actual=%s]", version, ct.Version())
	}
	rustVersion := vmeta.RustVersion
	if rustVersion == "" {
		// NOTE: Give a week's margin to allow for toolchain upgrades. Maybe raise.
		rustVersion, err = reg.RustVersionAt(vmeta.Updated.Add(-7 * 24 * time.Hour))
		if err != nil {
			return nil, errors.New("rust version heuristic failed")
		}
	} else if strings.Count(rustVersion, ".") == 1 {
		rustVersion += ".0"
	}
	// Apply structural hints to ensure minimum Rust version requirements are met
	// In the presence of options, (arbitrarily) prefer the latest Rust version in the range
	topLevel := t.Package + "-" + vmeta.Version.Version
	cargoTomlText, err := getFileFromCrate(bytes.NewReader(b), topLevel+"/Cargo.toml")
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream Cargo.toml")
	}
	minVer, maxVer := detectRustVersionBounds(string(cargoTomlText))
	if minVer != "" && semver.Cmp(rustVersion, minVer) < 0 {
		rustVersion = minVer
	}
	if maxVer != "" && semver.Cmp(rustVersion, maxVer) > 0 {
		rustVersion = maxVer
	}
	lockContent, err := getFileFromCrate(bytes.NewReader(b), topLevel+"/Cargo.lock")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream Cargo.lock")
	}
	// Extract package names from Cargo.lock for git-based index support
	var indexCommit string
	var packageNames []string
	if lockContent != nil && semver.Cmp(rustVersion, "1.34.0") >= 0 {
		// Registry search is only usable in builds that support the local or sparse registry protocols.
		// Sparse support: http://releases.rs/docs/1.68.0/#cargo
		// Local support: http://releases.rs/docs/1.34.0/#cargo
		stub, err := getRegistryStub(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to access registry query stub")
		}
		resp, err := stub(ctx, cratesregistryservice.FindRegistryCommitRequest{
			LockfileBase64: base64.StdEncoding.EncodeToString(lockContent),
			PublishedTime:  vmeta.Updated.Format(time.RFC3339),
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to call registry service")
		}
		if resp.CommitHash == "" {
			return nil, errors.New("no suitable registry commit found")
		}
		indexCommit = resp.CommitHash
		// TODO: If we want to default to sparse registry, we can predicate this on `if semver.Cmp(rustVersion, "1.68.0") < 0`
		// If only local registry supported, parse package names from Cargo.lock.
		packages, err := cargolock.Parse(string(lockContent))
		if err != nil {
			return nil, errors.Wrap(err, "[INTERNAL] failed to parse Cargo.lock")
		}
		packageSet := make(map[string]bool)
		for _, pkg := range packages {
			packageSet[pkg.Name] = true
		}
		for pkgName := range packageSet {
			packageNames = append(packageNames, pkgName)
		}
		slices.Sort(packageNames)
	}
	// TODO: This should be moved to build-time since strategies are intended to be, at least notionally, distro-independent.
	hasMUSLBuild, err := reg.HasMUSLBuild(rustVersion)
	if err != nil {
		return nil, errors.Wrap(err, "rust version compatibility check failed")
	}
	if !hasMUSLBuild {
		return nil, errors.New("rust version unsupported in MUSL builds")
	}

	return &CratesIOCargoPackage{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Ref:  ref,
			Dir:  dir,
		},
		RustVersion:    rustVersion,
		RegistryCommit: indexCommit,
		PackageNames:   packageNames,
	}, nil
}

func getFileFromCrate(crate io.Reader, path string) ([]byte, error) {
	gzr, err := gzip.NewReader(crate)
	if err != nil {
		return nil, errors.Wrapf(err, "initializing gzip reader")
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break // End of archive
			}
			return nil, err
		}
		if header.Name == path {
			return io.ReadAll(tr)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, err
		}
	}
	return nil, fs.ErrNotExist
}

// findAndValidateCargoTOML ensures the package config has the expected name and version, or finds a new version if necessary.
func findAndValidateCargoTOML(repo *git.Repository, c *object.Commit, name, version, guess string) (string, error) {
	t, _ := c.Tree()
	path := path.Join(guess, "Cargo.toml")
	orig, err := getCargoTOML(t, path)
	cargoTOML := &orig
	// TODO: Validate workspace version.
	if err != nil || cargoTOML.Name != name || (cargoTOML.Version() != version && cargoTOML.Version() != reg.WorkspaceVersion) {
		cargoTOML, path, err = findCargoTOML(repo, c, name)
	}
	if err == object.ErrFileNotFound {
		return path, errors.Errorf("Cargo.toml file not found [path=%s]", guess)
	} else if _, ok := err.(*toml.DecodeError); ok {
		return path, errors.Wrapf(err, "failed to parse Cargo.toml")
	} else if err != nil {
		return path, errors.Wrapf(err, "unknown Cargo.toml error")
	} else if cargoTOML.Name != name {
		return path, errors.Errorf("mismatched name [expected=%s,actual=%s,path=%s]", name, cargoTOML.Name, guess)
	} else if cargoTOML.Version() != version && cargoTOML.Version() != reg.WorkspaceVersion {
		return path, errors.Errorf("mismatched version [expected=%s,actual=%s]", version, cargoTOML.Version())
	}
	return path, nil
}

func findCargoTOML(repo *git.Repository, c *object.Commit, pkg string) (*reg.CargoTOML, string, error) {
	t, _ := c.Tree()
	path := "Cargo.toml"
	ct, err := getCargoTOML(t, path)
	if err == object.ErrFileNotFound {
		log.Printf("Searching repo after ./Cargo.toml not found")
	} else if _, ok := err.(*toml.DecodeError); ok {
		log.Printf("Searching repo after ./Cargo.toml decode error: %v", err)
	} else if err != nil {
		return nil, "", err
	} else if pkg == ct.Name {
		return &ct, path, nil
	} else {
		log.Printf("Searching repo after ./Cargo.toml name mismatch")
	}
	grs, err := repo.Grep(&git.GrepOptions{
		CommitHash: c.Hash,
		PathSpecs:  []*regexp.Regexp{regexp.MustCompile(".*/Cargo.toml$")},
		Patterns:   []*regexp.Regexp{regexp.MustCompile(fmt.Sprintf(`name\s*=\s*"%s"`, pkg))},
	})
	if err != nil {
		return nil, "", err
	}
	var names []string
	var cargoTOMLs []*reg.CargoTOML
	for _, gr := range grs {
		ct, err := getCargoTOML(t, gr.FileName)
		if err != nil {
			continue
		}
		if pkg == ct.Name {
			names = append(names, gr.FileName)
			cargoTOMLs = append(cargoTOMLs, &ct)
		}
	}
	if len(names) > 0 {
		if len(names) > 1 {
			log.Printf("Multiple Cargo.toml file candidates [pkg=%s,ref=%s,matches=%v]\n", pkg, c.Hash.String(), names)
		}
		return cargoTOMLs[0], names[0], nil
	}
	return nil, "", errors.Errorf("Cargo.toml heuristic found no matches")
}

func cargoTOMLSearch(pkg, path string, repo *git.Repository) (tm map[string]string, err error) {
	tm = make(map[string]string)
	commitIter, err := repo.Log(&git.LogOptions{
		Order:      git.LogOrderCommitterTime,
		PathFilter: func(s string) bool { return s == path },
		All:        true,
	})
	if err != nil {
		return nil, err
	}
	duplicates := make(map[string]string)
	err = commitIter.ForEach(func(c *object.Commit) error {
		t, err := c.Tree()
		if err != nil {
			return err
		}
		ct, err := getCargoTOML(t, path)
		if err != nil {
			if _, ok := err.(*toml.DecodeError); ok {
				return nil
			}
			return err
		}
		if ct.Name != pkg {
			// TODO: Handle the case where the package name has changed.
			log.Printf("Package name mismatch [expected=%s,actual=%s,path=%s,ref=%s]\n", pkg, ct.Name, path, c.Hash.String())
			return nil
		}
		ver := ct.Version()
		if ver == "" {
			return nil
		}
		if ver == reg.WorkspaceVersion {
			// TODO: Add support for workspace versioning.
			return nil
		}
		// If any are the same, return nil. (merges would create duplicates.)
		var foundMatch bool
		err = c.Parents().ForEach(func(c *object.Commit) error {
			t, err := c.Tree()
			if err != nil {
				return err
			}
			ct, err := getCargoTOML(t, path)
			if err != nil {
				// TODO: Detect and record file moves.
				return nil
			}
			if ct.Name == pkg && ct.Version() == ver {
				foundMatch = true
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !foundMatch {
			if tm[ver] != "" {
				// NOTE: This ignores commits processed later sequentially. Empirically, this seems to pick the better commit.
				if duplicates[ver] != "" {
					duplicates[ver] = fmt.Sprintf("%s,%s", duplicates[ver], c.Hash.String())
				} else {
					duplicates[ver] = fmt.Sprintf("%s,%s", tm[ver], c.Hash.String())
				}
			} else {
				tm[ver] = c.Hash.String()
			}
		}
		return nil
	})
	if len(duplicates) > 0 {
		for ver, dupes := range duplicates {
			log.Printf("Multiple matches found [pkg=%s,ver=%s,refs=%v]\n", pkg, ver, dupes)
		}
	}
	return tm, err
}

func getRegistryStub(ctx context.Context) (api.StubT[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse], error) {
	stubValue := ctx.Value(rebuild.CratesRegistryStubID)
	if stubValue == nil {
		return nil, errors.New("crates registry stub not found in context")
	}
	stub, ok := stubValue.(api.StubT[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse])
	if !ok {
		return nil, errors.New("invalid crates registry stub type in context")
	}
	return stub, nil
}
