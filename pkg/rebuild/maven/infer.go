// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/pkg/errors"
)

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	pom, err := NewPomXML(ctx, t, mux)
	if err != nil {
		return "", err
	}
	return uri.CanonicalizeRepoURI(pom.Repo())
}

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, fs billy.Filesystem, s storage.Storer) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, s, fs, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
		return r, nil
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	// Do pom.xml search.
	head, _ := repoConfig.Repository.Head()
	commitObject, _ := repoConfig.Repository.CommitObject(head.Hash())
	_, pkgPath, err := findPomXML(commitObject, t.Package)
	if err != nil {
		log.Printf("pom.xml path heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, repoConfig.URI, err.Error())
	}

	// Do version heuristic search.
	refMap, err := pomXMLSearch(t.Package, pkgPath, repoConfig.Repository)
	if err != nil {
		log.Printf("pom.xml version heuristic failed [pkg=%s,repo=%s]: %s\n", t.Package, repoConfig.URI, err.Error())
	}

	name, version := t.Package, t.Version
	cfg := &MavenBuild{}
	dir := path.Dir(pkgPath)
	pomXMLGuess := refMap[version]
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return cfg, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}

	var ref string
	var commit *object.Commit
	switch {
	case tagGuess != "":
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(tagGuess))
		if err == nil {
			if newPath, err := findAndValidatePomXML(commit, name, version, dir); err != nil {
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
			return cfg, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from tag [repo=%s,ref=%s]", repoConfig.URI, tagGuess)
		}
		fallthrough
	case pomXMLGuess != "":
		commit, err = repoConfig.Repository.CommitObject(plumbing.NewHash(pomXMLGuess))
		if err == nil {
			if newPath, err := findAndValidatePomXML(commit, name, version, dir); err != nil {
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
			return cfg, errors.Wrapf(err, "[INTERNAL] Failed ref resolve from git log [repo=%s,ref=%s]", repoConfig.URI, pomXMLGuess)
		}
		fallthrough
	default:
		if tagGuess == "" && pomXMLGuess == "" {
			return cfg, errors.Errorf("no git ref")
		}
		return cfg, errors.Errorf("no valid git ref")
	}
	jdk, err := getJarJDK(ctx, name, version, mux)
	if err != nil {
		return cfg, errors.Wrap(err, "fetching JDK")
	}
	if jdk == "" {
		return cfg, errors.New("no JDK found")
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

// getJarJDK gets the JDK version that is used to compile the original artifact on registry.
func getJarJDK(ctx context.Context, name, version string, mux rebuild.RegistryMux) (string, error) {
	releaseFile, err := mux.Maven.ReleaseFile(ctx, name, version, maven.TypeJar)
	if err != nil {
		return "", errors.Wrap(err, "fetching jar file")
	}
	jarBytes, err := io.ReadAll(releaseFile)
	if err != nil {
		return "", errors.Wrap(err, "reading jar file")
	}
	zipReader, err := zip.NewReader(bytes.NewReader(jarBytes), int64(len(jarBytes)))
	if err != nil {
		return "", errors.Wrap(err, "unzipping jar file")
	}
	jdk, err := inferJDKFromManifest(zipReader)
	if err != nil {
		return "", errors.Wrap(err, "inferring JDK from manifest")
	}
	if jdk != "" {
		return jdk, nil
	}
	jdkInt, err := inferJDKFromBytecode(zipReader)
	if err != nil {
		return "", errors.Wrap(err, "inferring JDK from bytecode")
	}
	return fmt.Sprintf("%d", jdkInt), nil
}

// inferJDKFromManifest extracts the JDK version from the MANIFEST.MF file in the JAR.
func inferJDKFromManifest(zipReader *zip.Reader) (string, error) {
	manifestFile, err := zipReader.Open("META-INF/MANIFEST.MF")
	if err != nil {
		return "", errors.Wrap(err, "opening manifest file")
	}
	defer manifestFile.Close()

	manifestReader, err := io.ReadAll(manifestFile)
	if err != nil {
		return "", errors.Wrap(err, "reading manifest file")
	}
	for _, line := range strings.Split(string(manifestReader), "\n") {
		if strings.HasPrefix(line, "Build-Jdk:") || strings.HasPrefix(line, "Build-Jdk-Spec:") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}

// inferJDKFromBytecode identifies the lowest JDK version that can run the provided JAR's bytecode.
func inferJDKFromBytecode(jarZip *zip.Reader) (int, error) {
	for _, file := range jarZip.File {
		if strings.HasSuffix(file.Name, ".class") {
			classFile, err := file.Open()
			if err != nil {
				continue
			}
			defer classFile.Close()
			classBytes, err := io.ReadAll(classFile)
			if err != nil {
				continue
			}
			majorVersion, err := getClassFileMajorVersion(classBytes)
			if err != nil {
				return 0, errors.Wrap(err, "parsing class file for major version")
			}
			return majorVersion, nil
		}
	}
	return 0, errors.New("no .class files found in jar")
}

// getClassFileMajorVersion extracts the major version from Java class file bytes
func getClassFileMajorVersion(classBytes []byte) (int, error) {
	if len(classBytes) < 8 {
		return 0, errors.New("class file too short")
	}
	// Check magic number (0xCAFEBABE)
	if classBytes[0] != 0xCA || classBytes[1] != 0xFE || classBytes[2] != 0xBA || classBytes[3] != 0xBE {
		return 0, errors.New("invalid class file magic number")
	}
	// Skip minor version (bytes 4-5) as it is always 0 since Java 1.1 and read major version (bytes 6-7)
	// JDK and classfile versions: https://javaalmanac.io/bytecode/versions/
	// Position of bytes for version in classfile: https://docs.oracle.com/javase/specs/jvms/se21/html/jvms-4.html
	bytecodeToVersionOffset := uint16(44)
	majorVersion := (uint16(classBytes[6]) << 8) | uint16(classBytes[7]) - bytecodeToVersionOffset
	return int(majorVersion), nil
}

// findAndValidatePomXML ensures the package config has the expected name and version,
// or finds a new version if necessary.
func findAndValidatePomXML(commit *object.Commit, name, version, dir string) (string, error) {
	commitTree, _ := commit.Tree()
	path := path.Join(dir, "pom.xml")
	orig, err := getPomXML(commitTree, path)
	pomXML := &orig
	if err == object.ErrFileNotFound {
		pomXML, path, err = findPomXML(commit, name)
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

func findPomXML(commit *object.Commit, pkg string) (*PomXML, string, error) {
	commitTree, _ := commit.Tree()
	var names []string
	var pomXMLs []PomXML
	commitTree.Files().ForEach(func(f *object.File) error {
		if !strings.HasSuffix(f.Name, "pom.xml") {
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
