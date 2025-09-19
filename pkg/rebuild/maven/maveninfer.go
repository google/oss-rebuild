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
	head, _ := repoConfig.Repository.Head()
	commitObject, _ := repoConfig.Repository.CommitObject(head.Hash())

	_, pkgPath, err := findPomXML(commitObject, t.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "build manifest heuristic failed")
	}

	refMap, err := pomXMLSearch(t.Package, pkgPath, repoConfig.Repository)
	if err != nil {
		log.Printf("pom.xml version heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, repoConfig.URI, err.Error())
	}
	name, version := t.Package, t.Version
	var dir string
	pomXMLGuess := refMap[version]
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}

	sourceJarGuess, err := findClosestCommitToSource(ctx, t, mux, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "source jar heuristic failed")
	}

	var ref string
	var commit *object.Commit
	switch {
	case tagGuess != "":
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidatePomXML(commit, name, version, pkgPath); err != nil {
				log.Printf("registry heuristic tag invalid: %v", err)
			} else {
				log.Printf("using tag heuristic ref: %s", tagGuess[:9])
				ref = tagGuess
				dir = filepath.Dir(newPath)
				break
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
			if newPath, err := findAndValidatePomXML(commit, name, version, pkgPath); err != nil {
				log.Printf("registry heuristic git log invalid: %v", err)
			} else {
				log.Printf("using git log heuristic ref: %s", pomXMLGuess[:9])
				ref = pomXMLGuess
				dir = filepath.Dir(newPath)
				break
			}
		} else if err == plumbing.ErrObjectNotFound {
			log.Printf("git log heuristic ref not found in repo")
		} else {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", repoConfig.URI, pomXMLGuess)
		}
		fallthrough
	case sourceJarGuess != nil:
		log.Printf("using source jar heuristic ref: %s", ref[:9])
		// Only validate name, since version is sometimes not updated in pom.xml.
		_, newPath, err := findPomXML(commitObject, name)
		if err != nil {
			return nil, errors.Wrapf(err, "pom.xml heuristic failed")
		}
		dir = filepath.Dir(newPath)
		ref = sourceJarGuess.Hash.String()
	default:
		if pomXMLGuess == "" && tagGuess == "" {
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

// findAndValidatePomXML ensures the package config has the expected name and version,
// or finds a new version if necessary.
func findAndValidatePomXML(commit *object.Commit, name, version, pomPath string) (string, error) {
	commitTree, _ := commit.Tree()
	orig, err := getPomXML(commitTree, pomPath)
	pomXML := &orig
	if err == object.ErrFileNotFound {
		pomXML, pomPath, err = findPomXML(commit, name)
	}
	if err == object.ErrFileNotFound {
		return pomPath, errors.Errorf("pom.xml file not found [path=%s]", pomPath)
	} else if _, ok := err.(*xml.SyntaxError); ok {
		return pomPath, errors.Wrapf(err, "failed to parse pom.xml")
	} else if err != nil {
		return pomPath, errors.Wrapf(err, "unknown pom.xml error")
	} else if pomXML.Name() != name {
		return pomPath, errors.Errorf("mismatched name [expected=%s,actual=%s,path=%s]", name, pomXML.Name(), pomPath)
	} else if pomXML.Version() != version {
		return pomPath, errors.Errorf("mismatched version [expected=%s,actual=%s,path=%s]", version, pomXML.Version(), pomPath)
	}
	return pomPath, nil
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
