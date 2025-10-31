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

// MavenInfer attempts to find the correct git ref and build directory for a given Maven target.
func MavenInfer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig) (rebuild.Strategy, error) {
	// Try heuristics in order - tag, git log, source jar.
	// Commit is selected by first successful heuristic.
	// If a heuristic finds a commit but cannot find a matching POM, it falls through to the next heuristic.
	// If a heuristic finds a commit and a matching POM, it is selected as the commit.
	// If a heuristic does not find a commit, it falls through to the next heuristic.
	// If no heuristics find a commit, or none of the found commits have a matching POM, return an error.
	name, version := t.Package, t.Version
	// 1. Tag Heuristic
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	var dir, ref string
	var commit *object.Commit
	var msg string
	if tagGuess != "" {
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to get commit from tag [repo=%s,ref=%s]", repoConfig.URI, tagGuess)
		}
		if dir, msg = findBuildDir(commit, t); dir != "" {
			ref = tagGuess
		}
		log.Printf("using tag heuristic: %s", msg)
	}
	// 2. Git Log Heuristic
	var pomXMLGuess string
	if dir == "" {
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
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(pomXMLGuess))
		if err == nil {
			if dir, msg = findBuildDir(commit, t); dir != "" {
				ref = pomXMLGuess
			}
			log.Printf("using git log heuristic: %s", msg)
		} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", repoConfig.URI, pomXMLGuess)
		}
	}
	// 3. Source Jar Heuristic
	var sourceJarGuess *object.Commit
	if dir == "" {
		sourceJarGuess, err = findClosestCommitToSource(ctx, t, mux, repoConfig.Repository)
		if err != nil {
			log.Printf("source jar heuristic failed: %s", err)
		}
		if sourceJarGuess != nil {
			if dir, msg = findBuildDir(sourceJarGuess, t); dir != "" {
				ref = sourceJarGuess.Hash.String()
			}
			log.Printf("using source jar heuristic: %s", msg)
		}
	}
	if dir == "" {
		if pomXMLGuess != "" || tagGuess != "" || sourceJarGuess != nil {
			return nil, errors.Errorf("no valid git ref")
		}
		return nil, errors.Errorf("no git ref")
	}
	jdk, err := inferOrFallbackToDefaultJDK(ctx, t.Package, t.Version, mux)
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

// findBuildDir is a helper that checks if a given commit contains a valid pom.xml for the package.
// It returns the directory containing the pom.xml and a summary.
// If no valid pom.xml is found, it returns an empty directory and a summary indicating the failure reason.
func findBuildDir(commit *object.Commit, t rebuild.Target) (dir string, summary string) {
	pomXML, foundPkgPath, err := findPomXML(commit, t.Package)
	if err != nil {
		return "", fmt.Sprintf("could not find a pom.xml for the package in ref %s", commit.Hash.String()[:9])
	}
	dir = filepath.Dir(foundPkgPath)
	ref := commit.Hash.String()
	if pomXML.Version() != t.Version {
		return dir, fmt.Sprintf("with mismatched version [expected=%s,actual=%s,path=%s,ref=%s]", t.Version, pomXML.Version(), path.Join(dir, "pom.xml"), ref[:9])
	} else {
		return dir, fmt.Sprintf("with pkg and version match ref [version=%s,path=%s,ref=%s]", t.Version, path.Join(dir, "pom.xml"), ref[:9])
	}
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
