// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	"github.com/pkg/errors"
)

func getPackageJSON(tree *object.Tree, path string) (pkgJSON npmreg.PackageJSON, err error) {
	f, err := tree.File(path)
	if err != nil {
		return pkgJSON, err
	}
	p, err := f.Contents()
	if err != nil {
		return pkgJSON, err
	}
	return pkgJSON, json.Unmarshal([]byte(p), &pkgJSON)
}

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	vmeta, err := mux.NPM.Version(ctx, t.Package, t.Version)
	if err != nil {
		return "", err
	}
	return uri.CanonicalizeRepoURI(vmeta.Repository.URL)
}

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, fs billy.Filesystem, s storage.Storer) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, s, fs, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
	// Do package.json search.
	head, _ := r.Repository.Head()
	c, _ := r.Repository.CommitObject(head.Hash())
	_, pkgPath, err := findPackageJSON(r.Repository, c, t.Package)
	if err != nil {
		log.Printf("package.json path heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, r.URI, err.Error())
	}
	r.Dir = path.Dir(pkgPath)
	// Do version heuristic search.
	r.RefMap, err = pkgJSONSearch(t.Package, pkgPath, r.Repository)
	if err != nil {
		log.Printf("package.json version heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, r.URI, err.Error())
	}
	return r, nil
}

func PickNodeVersion(meta *npmreg.NPMVersion) (string, error) {
	version := meta.NodeVersion
	if version == "" {
		// TODO: Consider selecting based on release date.
		return "10.17.0", nil
	}
	nv, err := semver.New(version)
	if err != nil {
		return "", errors.Errorf("invalid node version: %s", version)
	}
	if nv.Compare(npmreg.UnofficialNodeReleases[0].Version) > 0 {
		// Trust the future
		return nv.String(), nil
	}
	var best npmreg.NodeRelease
	for _, r := range npmreg.UnofficialNodeReleases {
		if !r.HasMUSL {
			continue
		}
		if cmp := r.Version.Compare(nv); cmp == 0 {
			return r.Version.String(), nil
		} else if cmp < 0 {
			return best.Version.String(), nil
		}
		// Skip update if major.minor match but patch version is lower
		if !(best.Version.Major == r.Version.Major && best.Version.Minor == r.Version.Minor) {
			best = r
		}
	}
	return best.Version.String(), nil
}

func PickNPMVersion(meta *npmreg.NPMVersion) (string, error) {
	npmv := meta.NPMVersion
	if npmv == "" {
		// TODO: Guess based on upload date.
		return "", errors.New("No NPM version")
	}
	s, err := semver.New(npmv)
	if err != nil || s.Prerelease != "" || s.Build != "" {
		return "", errors.Errorf("Unsupported NPM version '%s'", npmv)
	}
	if s.Major < 5 {
		// NOTE: Upgrade all previous versions to 5.0.4 to fix incompatibilities.
		return "5.0.4", nil
	} else if s.Major == 5 && (s.Minor == 4 || s.Minor == 5) {
		// NOTE: Some versions of NPM 5 had issues with Node 9 and higher.
		// Fix: https://github.com/npm/npm/commit/c851bb503a756b7cd48d12ef0e12f39e6f30c577
		// Release: https://github.com/npm/npm/releases/tag/v5.6.0
		return "5.6.0", nil
	}
	return npmv, nil
}

func InferLocation(t rebuild.Target, vmeta *npmreg.NPMVersion, rcfg *rebuild.RepoConfig) (loc rebuild.Location, versionOverride string, err error) {
	// Initialize location with repo URI from config
	loc = rebuild.Location{
		Repo: rcfg.URI,
	}
	// Determine dir for build
	if vmeta.Directory != "" {
		if rcfg.Dir != "" && rcfg.Dir != vmeta.Directory {
			log.Printf("package.json path disagreement [metadata=%s,heuristic=%s]\n", vmeta.Directory, rcfg.Dir)
		}
		loc.Dir = vmeta.Directory
	} else if rcfg.Dir != "" {
		loc.Dir = rcfg.Dir
	} else {
		loc.Dir = "."
	}
	// Determine git ref to rebuild
	registryRef := vmeta.GitHEAD
	pkgJSONGuess := rcfg.RefMap[t.Version]
	tagGuess, err := rebuild.FindTagMatch(t.Package, t.Version, rcfg.Repository)
	if err != nil {
		return loc, "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	var c *object.Commit
	var badVersionRef string
	switch {
	case registryRef != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(registryRef))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, loc.Dir); err != nil {
				log.Printf("registry ref invalid: %v", err)
				if strings.HasPrefix(err.Error(), "mismatched version") {
					badVersionRef = registryRef
				}
			} else {
				log.Printf("using registry ref: %s", registryRef[:9])
				loc.Ref = registryRef
				loc.Dir = filepath.Dir(newPath)
				return loc, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("registry ref not found in repo")
		} else {
			return loc, "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from registry [repo=%s,ref=%s]", rcfg.URI, registryRef)
		}
		fallthrough
	case tagGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, loc.Dir); err != nil {
				log.Printf("registry heuristic tag invalid: %v", err)
				if strings.HasPrefix(err.Error(), "mismatched version") {
					badVersionRef = tagGuess
				}
			} else {
				log.Printf("using tag heuristic ref: %s", tagGuess[:9])
				loc.Ref = tagGuess
				loc.Dir = filepath.Dir(newPath)
				return loc, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("tag heuristic ref not found in repo")
		} else {
			return loc, "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", rcfg.URI, tagGuess)
		}
		fallthrough
	case pkgJSONGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(pkgJSONGuess))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, loc.Dir); err != nil {
				log.Printf("registry heuristic git log invalid: %v", err)
				// NOTE: Omit badVersionRef default since the existing heuristic should
				// never select a ref with the version mismatch.
			} else {
				log.Printf("using git log heuristic ref: %s", pkgJSONGuess[:9])
				loc.Ref = pkgJSONGuess
				loc.Dir = filepath.Dir(newPath)
				return loc, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("git log heuristic ref not found in repo")
		} else {
			return loc, "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", rcfg.URI, pkgJSONGuess)
		}
		fallthrough
	default:
		if badVersionRef != "" {
			log.Printf("using version override recovery: %s", badVersionRef[:9])
			c, _ = rcfg.Repository.CommitObject(plumbing.NewHash(badVersionRef))
			loc.Ref = badVersionRef
			versionOverride = t.Version
			return loc, versionOverride, nil
		} else if registryRef == "" && tagGuess == "" && pkgJSONGuess == "" {
			return loc, "", errors.Errorf("no git ref")
		} else {
			return loc, "", errors.Errorf("no valid git ref")
		}
	}
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	vmeta, err := mux.NPM.Version(ctx, name, version)
	if err != nil {
		return nil, err
	}
	npmv, err := PickNPMVersion(vmeta)
	if err != nil {
		return nil, err
	}
	var versionOverride string
	loc := rebuild.Location{Repo: rcfg.URI, Dir: rcfg.Dir}
	if lh, ok := hint.(*rebuild.LocationHint); hint != nil && !ok {
		return nil, errors.Errorf("unsupported hint type: %T", hint)
	} else if lh != nil && lh.Ref != "" {
		loc.Ref = lh.Ref
		if lh.Dir != "" {
			loc.Dir = lh.Dir
		}
	} else {
		loc, versionOverride, err = InferLocation(t, vmeta, rcfg)
		if err != nil {
			return nil, err
		}
	}
	c, err := rcfg.Repository.CommitObject(plumbing.NewHash(loc.Ref))
	if err != nil {
		return nil, err
	}
	tree, _ := c.Tree()
	// If the package.json contains a build script, run that script with its
	// required dependencies prior to `npm pack`.
	pkgJSON, err := getPackageJSON(tree, path.Join(loc.Dir, "package.json"))
	if err != nil {
		log.Println("error fetching package.json:", err.Error())
	} else if pkgJSON.Scripts != nil {
		_, hasPrepare := pkgJSON.Scripts["prepare"]
		_, hasPrepack := pkgJSON.Scripts["prepack"]
		// TODO: Detect similarly named scripts
		_, hasBuild := pkgJSON.Scripts["build"]
		if hasPrepack || hasPrepare || hasBuild {
			// TODO: Consider limiting this case to only packages with a 'dist/' dir.
			pmeta, err := mux.NPM.Package(ctx, name)
			if err != nil {
				return nil, errors.Wrap(err, "[INTERNAL] fetching package metadata")
			}
			ut, ok := pmeta.UploadTimes[version]
			if !ok {
				return nil, errors.Errorf("[INTERNAL] upload time not found")
			}
			nodeVersion, err := PickNodeVersion(vmeta)
			if err != nil {
				return nil, errors.Wrap(err, "[INTERNAL] picking node version")
			}
			// TODO: detect and install pnpm
			// TODO: detect and install yarn
			b := &NPMCustomBuild{
				NPMVersion:      npmv,
				NodeVersion:     nodeVersion,
				VersionOverride: versionOverride,
				RegistryTime:    ut,
				Location:        loc,
			}
			if hasBuild {
				b.Command = "build"
			}
			if !(hasPrepare || hasPrepack) {
				b.PrepackRemoveDeps = true
			}
			if v, _ := semver.New(npmv); v.Major <= 6 { // NOTE: PickNPMVersion guarantees a valid semver
				b.KeepRoot = true
			}
			return b, nil
		}
	}
	return &NPMPackBuild{
		NPMVersion:      npmv,
		VersionOverride: versionOverride,
		Location:        loc,
	}, nil
}

// findAndValidatePackageJSON ensures the package config has the expected name and version,
// or finds a new version if necessary.
func findAndValidatePackageJSON(repo *git.Repository, c *object.Commit, name, version, guess string) (string, error) {
	t, _ := c.Tree()
	path := path.Join(guess, "package.json")
	orig, err := getPackageJSON(t, path)
	pkgJSON := &orig
	if err != nil || pkgJSON.Name != name {
		pkgJSON, path, err = findPackageJSON(repo, c, name)
	}
	if err == object.ErrFileNotFound {
		return path, errors.Errorf("package.json file not found [path=%s]", guess)
	} else if _, ok := err.(*json.SyntaxError); ok {
		return path, errors.Wrapf(err, "failed to parse package.json")
	} else if err != nil {
		return path, errors.Wrapf(err, "unknown package.json error")
	} else if pkgJSON.Name != name {
		return path, errors.Errorf("mismatched name [expected=%s,actual=%s,path=%s]", name, pkgJSON.Name, guess)
	} else if pkgJSON.Version != version {
		return path, errors.Errorf("mismatched version [expected=%s,actual=%s]", version, pkgJSON.Version)
	}
	return path, nil
}

func findPackageJSON(repo *git.Repository, c *object.Commit, pkg string) (*npmreg.PackageJSON, string, error) {
	t, _ := c.Tree()
	wellKnownPaths := []string{
		"package.json",
		path.Join("packages", pkg[strings.IndexRune(pkg, '/')+1:], "package.json"),
	}
	for _, path := range wellKnownPaths {
		pkgJSON, err := getPackageJSON(t, path)
		if err != nil {
			if err == object.ErrFileNotFound {
				continue
			}
			if _, ok := err.(*json.SyntaxError); ok {
				continue
			}
			return nil, "", err
		}
		if pkg == pkgJSON.Name {
			return &pkgJSON, path, nil
		}
	}
	grs, err := repo.Grep(&git.GrepOptions{
		CommitHash: c.Hash,
		PathSpecs:  []*regexp.Regexp{regexp.MustCompile(".*/package.json$")},
		Patterns:   []*regexp.Regexp{regexp.MustCompile(fmt.Sprintf(`"name":\s*"%s"`, pkg))},
	})
	if err != nil {
		return nil, "", err
	}
	var names []string
	var pkgJSONs []npmreg.PackageJSON
	for _, gr := range grs {
		pkgJSON, err := getPackageJSON(t, gr.FileName)
		if err != nil {
			continue
		}
		if pkg == pkgJSON.Name {
			names = append(names, gr.FileName)
			pkgJSONs = append(pkgJSONs, pkgJSON)
		}
	}
	if len(names) > 0 {
		if len(names) > 1 {
			log.Printf("Multiple package.json file candidates [pkg=%s,ref=%s,matches=%v]\n", pkg, c.Hash.String(), names)
		}
		return &pkgJSONs[0], names[0], nil
	}
	return nil, "", errors.Errorf("package.json heuristic found no matches")
}

func pkgJSONSearch(pkg, pkgJSONPath string, repo *git.Repository) (tm map[string]string, err error) {
	tm = make(map[string]string)
	commitIter, err := repo.Log(&git.LogOptions{
		Order:      git.LogOrderCommitterTime,
		PathFilter: func(s string) bool { return s == pkgJSONPath },
		All:        true,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "searching for commits touching package.json")
	}
	duplicates := make(map[string]string)
	err = commitIter.ForEach(func(c *object.Commit) error {
		t, err := c.Tree()
		if err != nil {
			return errors.Wrapf(err, "fetching tree")
		}
		pkgJSON, err := getPackageJSON(t, pkgJSONPath)
		if _, ok := err.(*json.SyntaxError); ok {
			return nil // unable to parse
		} else if errors.Is(err, object.ErrFileNotFound) {
			return nil // file deleted at this commit
		} else if err != nil {
			return errors.Wrapf(err, "fetching package.json")
		}
		if pkgJSON.Name != pkg {
			// TODO: Handle the case where the package name has changed.
			log.Printf("Package name mismatch [expected=%s,actual=%s,path=%s,ref=%s]\n", pkg, pkgJSON.Name, pkgJSONPath, c.Hash.String())
			return nil
		}
		ver := pkgJSON.Version
		if ver == "" {
			return nil
		}
		// If any are the same, return nil. (merges would create duplicates.)
		var foundMatch bool
		err = c.Parents().ForEach(func(c *object.Commit) error {
			t, err := c.Tree()
			if err != nil {
				return errors.Wrapf(err, "fetching tree")
			}
			pkgJSON, err := getPackageJSON(t, pkgJSONPath)
			if err != nil {
				// TODO: Detect and record file moves.
				return nil
			}
			if pkgJSON.Name == pkg && pkgJSON.Version == ver {
				foundMatch = true
			}
			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "comparing against parent package.json")
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
	return
}
