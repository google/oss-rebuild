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

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	billy "github.com/go-git/go-billy/v5"
	"github.com/pkg/errors"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
)

type RepoConfig struct {
	*git.Repository
	URI    string
	Dir    string
	RefMap map[string]string
}

type BuildConfig struct {
	Repo string
	Dir  string
	Ref  string
	// TODO: Switch to an interface that can represent different build types.
	Build MavenBuild
}

type MavenBuild struct {
	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string
}

func getPomXML(tree *object.Tree, path string) (pomXML mavenreg.PomXML, err error) {
	f, err := tree.File(path)
	if err != nil {
		return
	}
	p, err := f.Contents()
	if err != nil {
		return
	}
	err = xml.Unmarshal([]byte(p), &pomXML)
	return
}

func doRepoInferenceAndClone(ctx context.Context, name string, pomXML *mavenreg.PomXML, fs billy.Filesystem, s storage.Storer) (r RepoConfig, err error) {
	r.URI, err = uri.CanonicalizeRepoURI(pomXML.URL)
	if err != nil {
		return
	}
	r.Repository, err = rebuild.LoadRepo(ctx, name, s, fs, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
	case transport.ErrAuthenticationRequired:
		err = errors.Errorf("Repo invalid or private")
		return
	default:
		err = errors.Wrapf(err, "Clone failed [repo=%s]", r.URI)
		return
	}
	// Do pom.xml search.
	head, _ := r.Repository.Head()
	c, _ := r.Repository.CommitObject(head.Hash())
	_, pkgPath, err := findPomXML(r.Repository, c, name)
	if err != nil {
		log.Printf("pom.xml path heuristic failed [pkg=%s,repo=%s]: %s\n", name, r.URI, err.Error())
	}
	r.Dir = path.Dir(pkgPath)
	// Do version heuristic search.
	r.RefMap, err = pomXMLSearch(name, pkgPath, r.Repository)
	if err != nil {
		log.Printf("pom.xml version heuristic failed [pkg=%s,repo=%s]: %s\n", name, r.URI, err.Error())
	}
	return
}

func getJarJDK(name, version string) (string, error) {
	r, err := mavenreg.ReleaseFile(name, version, mavenreg.TypeJar)
	if err != nil {
		return "", errors.Wrap(err, "fetching jar file")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "reading jar file")
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", errors.Wrap(err, "unzipping jar file")
	}
	f, err := zr.Open("META-INF/MANIFEST.MF")
	if err != nil {
		return "", errors.Wrap(err, "opening manifest file")
	}
	defer f.Close()
	fr, err := io.ReadAll(f)
	if err != nil {
		return "", errors.Wrap(err, "reading manifest file")
	}
	for _, line := range strings.Split(string(fr), "\n") {
		if strings.HasPrefix(line, "Build-Jdk:") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}

func doInference(ctx context.Context, t rebuild.Target, rcfg *RepoConfig) (BuildConfig, error) {
	name, version := t.Package, t.Version
	var cfg BuildConfig
	dir := rcfg.Dir
	pomXMLGuess := rcfg.RefMap[version]
	tagGuess, err := rebuild.FindTagMatch(name, version, rcfg.Repository)
	if err != nil {
		return cfg, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	var ref string
	var c *object.Commit
	switch {
	case tagGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidatePomXML(rcfg.Repository, c, name, version, dir); err != nil {
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
			return cfg, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", rcfg.URI, tagGuess)
		}
		fallthrough
	case pomXMLGuess != "":
		c, err = rcfg.Repository.CommitObject(plumbing.NewHash(pomXMLGuess))
		if err == nil {
			if newPath, err := findAndValidatePomXML(rcfg.Repository, c, name, version, dir); err != nil {
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
			return cfg, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", rcfg.URI, pomXMLGuess)
		}
		fallthrough
	default:
		if tagGuess == "" && pomXMLGuess == "" {
			return cfg, errors.Errorf("no git ref")
		}
		return cfg, errors.Errorf("no valid git ref")
	}
	jdk, err := getJarJDK(name, version)
	if err != nil {
		return cfg, errors.Wrap(err, "fetching JDK")
	}
	if jdk == "" {
		return cfg, errors.New("no JDK found")
	}
	// TODO: Normalize JDK
	return BuildConfig{Dir: dir, Ref: ref, Build: MavenBuild{JDKVersion: jdk}}, nil
}

// findAndValidatePomXML ensures the package config has the expected name and version,
// or finds a new version if necessary.
func findAndValidatePomXML(repo *git.Repository, c *object.Commit, name, version, dir string) (string, error) {
	t, _ := c.Tree()
	path := path.Join(dir, "pom.xml")
	orig, err := getPomXML(t, path)
	pomXML := &orig
	if err == object.ErrFileNotFound {
		pomXML, path, err = findPomXML(repo, c, name)
	}
	if err == object.ErrFileNotFound {
		return path, errors.Errorf("pom.xml file not found [path=%s]", path)
	} else if _, ok := err.(*xml.SyntaxError); ok {
		return path, errors.Wrapf(err, "failed to parse pom.xml")
	} else if err != nil {
		return path, errors.Wrapf(err, "unknown pom.xml error")
	} else if pomXML.Name() != name {
		return path, errors.Errorf("mismatched name [expected=%s,actual=%s,path=%s]", name, pomXML.Name(), path)
	} else if pomXML.Version() != version {
		return path, errors.Errorf("mismatched version [expected=%s,actual=%s,path=%s]", version, pomXML.Version(), path)
	}
	return path, nil
}

func findPomXML(repo *git.Repository, c *object.Commit, pkg string) (*mavenreg.PomXML, string, error) {
	t, _ := c.Tree()
	var names []string
	var pomXMLs []mavenreg.PomXML
	t.Files().ForEach(func(f *object.File) error {
		if !strings.HasSuffix(f.Name, "pom.xml") {
			return nil
		}
		pomXML, err := getPomXML(t, f.Name)
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
			log.Printf("Multiple pom.xml file candidates [pkg=%s,ref=%s,matches=%v]\n", pkg, c.Hash.String(), names)
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
	return
}

// Infer produces a rebuild strategy from the available package metadata.
func Infer(ctx context.Context, name, version string, s storage.Storer, fs billy.Filesystem) (BuildConfig, error) {
	t := rebuild.Target{Ecosystem: rebuild.Maven, Package: name, Version: version}
	p, err := mavenreg.VersionPomXML(name, version)
	if err != nil {
		return BuildConfig{}, err
	}
	rcfg, err := doRepoInferenceAndClone(ctx, name, &p, fs, s)
	if err != nil {
		return BuildConfig{}, err
	}
	cfg, err := doInference(ctx, t, &rcfg)
	cfg.Repo = rcfg.URI
	return cfg, err
}
