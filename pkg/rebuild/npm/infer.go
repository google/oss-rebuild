// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	billy "github.com/go-git/go-billy/v5"
	git "github.com/go-git/go-git/v5"
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
		return
	}
	p, err := f.Contents()
	if err != nil {
		return
	}
	err = json.Unmarshal([]byte(p), &pkgJSON)
	return
}

func (Rebuilder) InferRepo(t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	vmeta, err := mux.NPM.Version(t.Package, t.Version)
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
		err = errors.Errorf("Repo invalid or private")
		return
	default:
		err = errors.Wrapf(err, "Clone failed [repo=%s]", r.URI)
		return
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
	return
}

func inferFromRepo(t rebuild.Target, vmeta *npmreg.NPMVersion, rcfg *rebuild.RepoConfig) (ref, dir, versionOverride string, err error) {
	// Determine dir for build.
	if vmeta.Directory != "" {
		if rcfg.Dir != "" && rcfg.Dir != vmeta.Directory {
			log.Printf("package.json path disagreement [metadata=%s,heuristic=%s]\n", vmeta.Directory, rcfg.Dir)
		}
		dir = vmeta.Directory
	} else if rcfg.Dir != "" {
		dir = rcfg.Dir
	} else {
		dir = "."
	}
	// Determine git ref to rebuild.
	registryRef := vmeta.GitHEAD
	pkgJSONGuess := rcfg.RefMap[t.Version]
	tagGuess, err := rebuild.FindTagMatch(t.Package, t.Version, rcfg.Repository)
	if err != nil {
		return "", "", "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	var c *object.Commit
	var badVersionRef string
	switch {
	case registryRef != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(registryRef))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry ref invalid: %v", err)
				if strings.HasPrefix(err.Error(), "mismatched version") {
					badVersionRef = registryRef
				}
			} else {
				log.Printf("using registry ref: %s", registryRef[:9])
				ref = registryRef
				dir = filepath.Dir(newPath)
				return ref, dir, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("registry ref not found in repo")
		} else {
			return "", "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from registry [repo=%s,ref=%s]", rcfg.URI, registryRef)
		}
		fallthrough
	case tagGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry heuristic tag invalid: %v", err)
				if strings.HasPrefix(err.Error(), "mismatched version") {
					badVersionRef = tagGuess
				}
			} else {
				log.Printf("using tag heuristic ref: %s", tagGuess[:9])
				ref = tagGuess
				dir = filepath.Dir(newPath)
				return ref, dir, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("tag heuristic ref not found in repo")
		} else {
			return "", "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", rcfg.URI, tagGuess)
		}
		fallthrough
	case pkgJSONGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(pkgJSONGuess))
		if err == nil {
			if newPath, err := findAndValidatePackageJSON(rcfg.Repository, c, t.Package, t.Version, dir); err != nil {
				log.Printf("registry heuristic git log invalid: %v", err)
				// NOTE: Omit badVersionRef default since the existing heuristic should
				// never select a ref with the version mismatch.
			} else {
				log.Printf("using git log heuristic ref: %s", pkgJSONGuess[:9])
				ref = pkgJSONGuess
				dir = filepath.Dir(newPath)
				return ref, dir, "", nil
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("git log heuristic ref not found in repo")
		} else {
			return "", "", "", errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", rcfg.URI, pkgJSONGuess)
		}
		fallthrough
	default:
		if badVersionRef != "" {
			log.Printf("using version override recovery: %s", badVersionRef[:9])
			c, _ = rcfg.Repository.CommitObject(plumbing.NewHash(badVersionRef))
			ref = badVersionRef
			versionOverride = t.Version
			return ref, dir, versionOverride, nil
		} else if registryRef == "" && tagGuess == "" && pkgJSONGuess == "" {
			return "", "", "", errors.Errorf("no git ref")
		} else {
			return "", "", "", errors.Errorf("no valid git ref")
		}
	}
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	vmeta, err := mux.NPM.Version(name, version)
	if err != nil {
		return nil, err
	}
	npmv := vmeta.NPMVersion
	if npmv == "" {
		// TODO: Guess based on upload date.
		return nil, errors.New("No NPM version")
	}
	if s, err := semver.New(npmv); s.Prerelease != "" || s.Build != "" || err != nil {
		return nil, errors.Errorf("Unsupported NPM version '%s'", npmv)
	}
	switch npmv[:2] {
	case "0.", "1.", "2.", "3.", "4.":
		// XXX: Upgrade all previous versions to 5.0.4 to fix incompatibilities.
		npmv = "5.0.4"
	case "5.":
		// NOTE: Some versions of NPM 5 had issues with Node 9 and higher.
		// Fix: https://github.com/npm/npm/commit/c851bb503a756b7cd48d12ef0e12f39e6f30c577
		// Release: https://github.com/npm/npm/releases/tag/v5.6.0
		if npmv[:4] == "5.5." || npmv[:4] == "5.4" {
			npmv = "5.6.0"
		}
	}
	var ref, dir, override string
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
		ref, dir, override, err = inferFromRepo(t, vmeta, rcfg)
		if err != nil {
			return nil, err
		}
	}
	c, err := rcfg.Repository.CommitObject(plumbing.NewHash(ref))
	if err != nil {
		return nil, err
	}
	tree, _ := c.Tree()
	// If the package.json contains a build script, run that script with its
	// required dependencies prior to `npm pack`.
	pkgJSON, err := getPackageJSON(tree, path.Join(dir, "package.json"))
	if err != nil {
		log.Println("error fetching package.json:", err.Error())
	} else if pkgJSON.Scripts != nil {
		// TODO: Expand beyond just scripts named "build".
		if _, ok := pkgJSON.Scripts["build"]; ok {
			// TODO: Consider limiting this case to only packages with a 'dist/' dir.
			pmeta, err := mux.NPM.Package(name)
			if err != nil {
				return nil, errors.Wrap(err, "[INTERNAL] fetching package metadata")
			}
			ut, ok := pmeta.UploadTimes[version]
			if !ok {
				return nil, errors.Errorf("[INTERNAL] upload time not found")
			}
			// TODO: detect and install pnpm
			// TODO: detect and install yarn
			return &NPMCustomBuild{
				NPMVersion:      npmv,
				NodeVersion:     vmeta.NodeVersion,
				VersionOverride: override,
				Command:         "build",
				RegistryTime:    ut,
				Location: rebuild.Location{
					Repo: rcfg.URI,
					Ref:  ref,
					Dir:  dir,
				},
			}, nil
		}
	}
	return &NPMPackBuild{
		NPMVersion:      npmv,
		VersionOverride: override,
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Ref:  ref,
			Dir:  dir,
		},
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
