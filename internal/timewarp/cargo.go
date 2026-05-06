// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/iofs"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
)

// handleCargoGitArchive handles requests for Cargo git index archives.
func (h Handler) handleCargoGitArchive(rw http.ResponseWriter, r *http.Request, ts string) error {
	// Hard-code the only available endpoint since we only serve the archive
	if r.URL.Path != "/index.git.tar" {
		return herror{errors.New("invalid path for cargogitarchive"), http.StatusBadRequest}
	}
	// Extract list of packages from X-Package-Names header
	packageNamesHeader := r.Header.Get("X-Package-Names")
	if packageNamesHeader == "" {
		return herror{errors.New("missing X-Package-Names header"), http.StatusBadRequest}
	}
	packageNames := strings.Split(packageNamesHeader, ",")
	for i := range packageNames {
		packageNames[i] = strings.TrimSpace(packageNames[i])
	}
	// The "timestamp" is actually a commit hash for the index
	indexCommit := ts
	if indexCommit == "" {
		return herror{errors.New("no commit hash set"), http.StatusBadRequest}
	}
	if !commitHashRegex.MatchString(indexCommit) {
		return herror{errors.New("invalid commit hash format"), http.StatusBadRequest}
	}
	// Create git archive with index files for requested packages
	archiveData, err := h.createCargoIndexArchive(indexCommit, packageNames)
	if err != nil {
		return herror{errors.Wrap(err, "creating cargo index archive"), http.StatusInternalServerError}
	}
	// Set response headers
	rw.Header().Set("Content-Type", "application/x-tar")
	rw.Header().Set("Content-Length", fmt.Sprintf("%d", len(archiveData)))
	// Write archive data
	if _, err := rw.Write(archiveData); err != nil {
		return herror{errors.Wrap(err, "writing archive response"), http.StatusInternalServerError}
	}
	return nil
}

// handleCargoSparse handles requests for the Cargo sparse registry index.
func (h Handler) handleCargoSparse(rw http.ResponseWriter, r *http.Request, ts string) error {
	// We handle the request directly without changing URL.
	// The "timestamp" is actually a commit hash.
	indexCommit := ts
	if indexCommit == "" {
		return herror{errors.New("no commit hash set"), http.StatusBadRequest}
	}
	if !commitHashRegex.MatchString(indexCommit) {
		return herror{errors.New("invalid commit hash format"), http.StatusBadRequest}
	}
	// config.json content shouldn't be used by the client but is checked for presence.
	if r.URL.Path == "/config.json" {
		if _, err := io.WriteString(rw, defaultCratesConfig); err != nil {
			return herror{errors.Wrap(err, "writing response"), http.StatusInternalServerError}
		}
		return nil
	}
	// Redirect to index repo
	redirectURL := cratesIndexURL.JoinPath(indexCommit, r.URL.Path[1:]).String()
	return herror{errors.New(redirectURL), http.StatusFound}
}

// createCargoIndexArchive creates a tar archive containing a git repository with index files for the specified packages.
func (h Handler) createCargoIndexArchive(indexCommit string, packageNames []string) ([]byte, error) {
	wfs := memfs.New()
	configFile, err := wfs.Create("config.json")
	if err != nil {
		return nil, errors.Wrap(err, "creating config.json")
	}
	if _, err := configFile.Write([]byte(defaultCratesConfig)); err != nil {
		return nil, errors.Wrap(err, "writing config.json")
	}
	if err := configFile.Close(); err != nil {
		return nil, errors.Wrap(err, "closing config.json")
	}
	// Fetch and add index files for each package
	for _, packageName := range packageNames {
		if err := h.addPackageToIndex(wfs, indexCommit, packageName); err != nil {
			return nil, errors.Wrapf(err, "adding package %s to index", packageName)
		}
	}
	// Create git repo from index files
	gitfs, _ := wfs.Chroot(".git")
	if err := makeGitRepo(wfs, gitfs); err != nil {
		return nil, err
	}
	// Create tar archive of the full checkout
	var buf bytes.Buffer
	tarWriter := tar.NewWriter(&buf)
	defer tarWriter.Close()
	if err := tarWriter.AddFS(iofs.New(wfs)); err != nil {
		return nil, errors.Wrap(err, "adding git repo to tar")
	}
	if err := tarWriter.Close(); err != nil {
		return nil, errors.Wrap(err, "closing tar writer")
	}
	return buf.Bytes(), nil
}

// addPackageToIndex fetches the index file for a package and adds it to the filesystem.
func (h Handler) addPackageToIndex(fs billy.Filesystem, indexCommit, packageName string) error {
	indexPath := index.EntryPath(packageName)
	indexURL := cratesIndexURL.JoinPath(indexCommit, indexPath).String()
	req, err := http.NewRequest(http.MethodGet, indexURL, nil)
	if err != nil {
		return errors.Wrap(err, "creating request")
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return errors.Wrap(err, "fetching index file")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("Package %s not found in index at commit %s (status %d)", packageName, indexCommit, resp.StatusCode)
		return nil // skip missing file
	}
	// Ensure directory exists
	if err := fs.MkdirAll(path.Dir(indexPath), 0755); err != nil {
		return errors.Wrap(err, "creating directory")
	}
	// Write index file
	if indexFile, err := fs.Create(indexPath); err != nil {
		return errors.Wrap(err, "creating index file")
	} else {
		defer indexFile.Close()
		if _, err := io.Copy(indexFile, resp.Body); err != nil {
			return errors.Wrap(err, "copying index content")
		}
	}
	return nil
}

func makeGitRepo(wfs billy.Filesystem, dotGit billy.Filesystem) error {
	storer := filesystem.NewStorage(dotGit, cache.NewObjectLRUDefault())
	repo, err := git.Init(storer, wfs)
	if err != nil {
		return errors.Wrap(err, "initializing git repository")
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return errors.Wrap(err, "getting worktree")
	}
	if _, err := worktree.Add("."); err != nil {
		return errors.Wrap(err, "adding files to git")
	}
	signature := &object.Signature{Name: "Timewarp", Email: "timewarp@localhost", When: time.Now()}
	_, err = worktree.Commit("Initial cargo index", &git.CommitOptions{Author: signature})
	if err != nil {
		return errors.Wrap(err, "creating commit")
	}
	return nil
}
