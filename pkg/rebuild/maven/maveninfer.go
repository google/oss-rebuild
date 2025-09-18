// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

func MavenInfer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	var pomXMLGuess string
	head, _ := repoConfig.Repository.Head()
	commitObject, _ := repoConfig.Repository.CommitObject(head.Hash())
	_, pkgPath, err := findPomXML(commitObject, t.Package)
	if err != nil {
		log.Printf("cannot build ref map manifest heuristic: %s", err)
	} else {
		refMap, err := pomXMLSearch(t.Package, pkgPath, repoConfig.Repository)
		if err != nil {
			log.Printf("git log heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, repoConfig.URI, err.Error())
		}
		pomXMLGuess = refMap[version]
		if pomXMLGuess == "" {
			log.Printf("git log heuristic found no matches [pkg=%s,ver=%s]\n", name, version)
		}
	}
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	sourceJarGuess, err := findClosestCommitToSource(ctx, t, mux, repoConfig.Repository)
	if err != nil {
		log.Printf("source jar heuristic failed: %s", err)
	}
	var dir string
	var ref string
	var commit *object.Commit
	// Try heuristics in order - tag, git log, source jar.
	// Commit is selected by first successful heuristic.
	// If a heuristic finds a commit but cannot find a matching POM, it falls through to the next heuristic.
	// If a heuristic finds a commit and a matching POM, it is selected as the commit.
	// If a heuristic does not find a commit, it falls through to the next heuristic.
	// If no heuristics find a commit, or none of the found commits have a matching POM, return an error.
	switch {
	case tagGuess != "":
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			// First, find the POM file by package name.
			pomXML, foundPkgPath, err := findPomXML(commit, t.Package)
			if err != nil {
				// No POM with package name, continue to next heuristic.
				log.Printf("tag heuristic failed: could not find a pom.xml for the package")
			} else {
				// A package match was found, so set it as our best guess.
				ref = tagGuess
				dir = filepath.Dir(foundPkgPath)
				log.Printf("using tag heuristic (pkg match) ref: %s", tagGuess[:9])
				// Now, try to validate with the version for a more precise match.
				if pomXML.Version() != version {
					log.Printf("using tag heuristic with mismatched version [expected=%s,actual=%s,path=%s,ref=%s]", version, pomXML.Version(), path.Join(dir, "pom.xml"), tagGuess[:9])
				} else {
					log.Printf("using tag heuristic (pkg and version match) ref: %s", tagGuess[:9])
				}
				break // Match found, exit switch.
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("tag heuristic ref not found in repo")
		} else {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", repoConfig.URI, tagGuess)
		}
		fallthrough
	case pomXMLGuess != "":
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(pomXMLGuess))
		if err == nil {
			pomXML, foundPkgPath, err := findPomXML(commit, t.Package)
			if err != nil {
				log.Printf("git log heuristic failed: could not find a pom.xml for the package")
			} else {
				ref = pomXMLGuess
				dir = filepath.Dir(foundPkgPath)
				if pomXML.Version() != version {
					log.Printf("using git log heuristic with mismatched version [expected=%s,actual=%s,path=%s,ref=%s]", version, pomXML.Version(), path.Join(dir, "pom.xml"), pomXMLGuess[:9])
				} else {
					log.Printf("using git log heuristic (pkg and version match) ref: %s", pomXMLGuess[:9])
				}
				break
			}

		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("git log heuristic ref not found in repo")
		} else {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", repoConfig.URI, pomXMLGuess)
		}
		fallthrough
	case sourceJarGuess != nil:
		commit = sourceJarGuess
		pomXML, foundPkgPath, err := findPomXML(commit, t.Package)
		if err != nil {
			log.Printf("source jar heuristic failed: could not find a pom.xml for the package")
		} else {
			ref = sourceJarGuess.Hash.String()
			dir = filepath.Dir(foundPkgPath)
			if pomXML.Version() != version {
				log.Printf("using source jar heuristic with mismatched version [expected=%s,actual=%s,path=%s,ref=%s]", version, pomXML.Version(), path.Join(dir, "pom.xml"), ref[:9])
			} else {
				log.Printf("using source jar heuristic (pkg and version match) ref: %s", ref[:9])
			}
			break
		}
		fallthrough
	default:
		if pomXMLGuess != "" || tagGuess != "" || sourceJarGuess != nil {
			return nil, errors.Errorf("no valid git ref")
		}
		return nil, errors.Errorf("no git ref")
	}
	jdk, err := inferOrFallbackToDefaultJDK(ctx, name, version, mux)
	if err != nil {
		return nil, errors.Wrap(err, "fetching JDK")
	}
	return &MavenBuild{
		Location: rebuild.Location{
			Repo: repoConfig.URI,
			Dir:  dir,
			Ref:  ref,
		},
		JDKVersion: jdk,
	}, nil
}

func findPomXML(commit *object.Commit, pkg string) (*PomXML, string, error) {
	commitTree, _ := commit.Tree()
	var names []string
	var pomXMLs []PomXML
	commitTree.Files().ForEach(func(f *object.File) error {
		// Per Maven conventions, skip non-"pom.xml" files and those inside a `src` directory (unlikely to contain metadata).
		// Reference: https://maven.apache.org/guides/introduction/introduction-to-the-standard-directory-layout.html
		// Note: This will miss pom files, with name other than "pom.xml", whose paths are explicitly passed during build via "-f".
		if path.Base(f.Name) != "pom.xml" || strings.HasPrefix(f.Name, "src/") || strings.Contains(f.Name, "/src/") {
			return nil
		}
		pomXML, err := getPomXML(commitTree, f.Name)
		if err != nil {
			// XXX: ignore parse errors
			return nil
		}
		if pkg == pomXML.Name() {
			names = append(names, f.Name)
			pomXMLs = append(pomXMLs, pomXML)
		}
		return nil
	})
	if len(names) > 0 {
		if len(names) > 1 {
			log.Printf("Multiple pom.xml file candidates [pkg=%s,ref=%s,matches=%v]\n", pkg, commit.Hash.String(), names)
		}
		return &pomXMLs[0], names[0], nil
	}
	return nil, "", errors.Errorf("pom.xml heuristic found no matches")
}

func pomXMLSearch(name, pomXMLPath string, repo *git.Repository) (tm map[string]string, err error) {
	tm = make(map[string]string)
	commitIter, err := repo.Log(&git.LogOptions{
		Order:      git.LogOrderCommitterTime,
		PathFilter: func(s string) bool { return s == pomXMLPath },
		All:        true,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "searching for commits touching pom.xml")
	}
	duplicates := make(map[string]string)
	err = commitIter.ForEach(func(c *object.Commit) error {
		t, err := c.Tree()
		if err != nil {
			return errors.Wrapf(err, "fetching tree")
		}
		pomXML, err := getPomXML(t, pomXMLPath)
		if _, ok := err.(*xml.SyntaxError); ok {
			return nil // unable to parse
		} else if errors.Is(err, object.ErrFileNotFound) {
			return nil // file deleted at this commit
		} else if err != nil {
			return errors.Wrapf(err, "fetching pom.xml")
		}
		if pomXML.Name() != name {
			// TODO: Handle the case where the package name has changed.
			log.Printf("Package name mismatch [expected=%s,actual=%s,path=%s,ref=%s]\n", name, pomXML.Name(), pomXMLPath, c.Hash.String())
			return nil
		}
		ver := pomXML.Version()
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
			pomXML, err := getPomXML(t, pomXMLPath)
			if err != nil {
				// TODO: Detect and record file moves.
				return nil
			}
			if pomXML.Name() == name && pomXML.Version() == ver {
				foundMatch = true
			}
			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "comparing against parent pom.xml")
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
			log.Printf("Multiple matches found [pkg=%s,ver=%s,refs=%v]\n", name, ver, dupes)
		}
	}
	return tm, err
}

func getPomXML(tree *object.Tree, path string) (pomXML PomXML, err error) {
	f, err := tree.File(path)
	if err != nil {
		return pomXML, err
	}
	p, err := f.Contents()
	if err != nil {
		return pomXML, err
	}
	return pomXML, xml.Unmarshal([]byte(p), &pomXML)
}
