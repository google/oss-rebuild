// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package timewarp implements a registry-fronting HTTP service that filters returned content by time.
//
// This functionality allows us to transparently adjust the data returned to
// package manager clients to reflect the state of the registry at a given
// point in time (esp. a prior build time).
//
// When run on a local port, an example invocation for NPM would be:
//
//	npm --registry "http://npm:2015-05-13T10:31:26.370Z@localhost:8081" install
package timewarp

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
)

var (
	npmRegistry         = urlx.MustParse("https://registry.npmjs.org/")
	pypiRegistry        = urlx.MustParse("https://pypi.org/")
	cratesIndexURL      = urlx.MustParse("https://raw.githubusercontent.com/rust-lang/crates.io-index")
	lowTimeBound        = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	commitHashRegex     = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	defaultCratesConfig = `{"dl": "https://static.crates.io/crates","api": "/"}`
)

func parseTime(ts string) (*time.Time, error) {
	if ts == "" {
		return nil, errors.New("no time set")
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return nil, errors.New("invalid time set")
	}
	if t.Before(lowTimeBound) {
		return nil, errors.New("time set too far in the past")
	}
	if t.After(time.Now().Add(24 * time.Hour)) {
		return nil, errors.New("time set too far in the future")
	}
	return &t, nil
}

// Handler implements a registry-fronting HTTP service that filters returned content by time.
type Handler struct {
	Client httpx.BasicClient
}

var _ http.Handler = &Handler{}

type herror struct {
	error
	status int
}

func (h Handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if err := h.handleRequest(rw, r); err != nil {
		status := http.StatusInternalServerError
		if he, ok := err.(herror); ok {
			status = he.status
		}
		if status/100 == 3 {
			http.Redirect(rw, r, err.Error(), status)
			return
		}
		log.Printf("error: %+v  [%s]", err, r.URL.String())
		if status/100 == 4 { // Only surface messages for 4XX errors
			http.Error(rw, err.Error(), status)
		} else {
			http.Error(rw, http.StatusText(status), status)
		}
	}
}

func (h Handler) handleRequest(rw http.ResponseWriter, r *http.Request) error {
	// Expect to be called with a basic auth username and password of the form:
	// http://<platform>:<RFC3339>@<hostname>/
	// These populate the Authorization header with a "Basic" mode value and are
	// accessible here via Request.BasicAuth.
	platform, ts, _ := r.BasicAuth()
	switch platform {
	case "npm":
		r.URL.Host = npmRegistry.Host
		r.URL.Scheme = npmRegistry.Scheme
	case "pypi":
		r.URL.Host = pypiRegistry.Host
		r.URL.Scheme = pypiRegistry.Scheme
	// TODO: We should add cargogit which serves the repo from a given set of packages. This is built into go-git v6.
	case "cargogitarchive":
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
	case "cargosparse":
		// For rust, we'll handle the request directly without changing URL
		// The "timestamp" is actually a commit hash
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
	default:
		return herror{errors.New("unsupported platform"), http.StatusBadRequest}
	}
	{
		unescaped, err := url.QueryUnescape(ts)
		if err == nil && unescaped != ts {
			ts = unescaped
		}
	}
	t, err := parseTime(ts)
	if err != nil {
		return herror{err, http.StatusBadRequest}
	}
	// Determine whether to reroute the request based on the path structure.
	{
		parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
		switch {
		// Reference: https://github.com/npm/registry/blob/master/docs/REGISTRY-API.md
		case platform == "npm" && len(parts) == 1 && parts[0] != "": // /{pkg}
		case platform == "npm" && len(parts) == 2 && strings.HasPrefix(parts[0], "@"): // /@{org}/{pkg}
		// Reference: https://warehouse.pypa.io/api-reference/json.html
		case platform == "pypi" && len(parts) == 3 && parts[0] == "pypi" && parts[2] == "json": // /pypi/{pkg}/json
		default:
			http.Redirect(rw, r, r.URL.String(), http.StatusFound)
			return nil
		}
	}
	// Create a new request based on the provided method, path, and body but
	// directed at the upstream registry.
	nr, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
	// Configure headers for upstream registry request.
	{
		nr.Header = r.Header.Clone()
		// Remove the basic auth header set with the timewarp params.
		nr.Header.Del("Authorization")
		// Let our HTTP client set the encoding to use (by default, gzip) and
		// transparently decode it in the response.
		nr.Header.Del("Accept-Encoding")
		// The application/vnd.npm.install-v1 content type indicates that this must
		// be an NPM install request. However for NPM API requests, this install-v1
		// data format does not contain the requisite fields to filter by time. For
		// these cases, we attempt to downgrade to the more complete
		// application/json content type if the client allows it.
		if a := nr.Header.Get("Accept"); strings.Contains(a, "application/vnd.npm.install-v1+json") {
			if !strings.Contains(a, "application/json") {
				// TODO: We can support this case by adding a translation from the
				// application/json response ourselves but current client behavior does
				// not (yet) require it.
				return herror{errors.Errorf("unsupported Accept header: %s", a), http.StatusBadGateway}
			}
			nr.Header.Set("Accept", "application/json")
		}
	}
	resp, err := h.Client.Do(nr)
	if err != nil {
		return herror{errors.Wrap(err, "creating client"), http.StatusBadGateway}
	}
	defer resp.Body.Close()
	// Copy the registry response to the output, applying the time warp
	// transformation for relevant responses.
	for key, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
	if resp.StatusCode != 200 {
		rw.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(rw, resp.Body); err != nil {
			log.Printf("error: %+v", errors.Wrap(err, "transmitting non-ok response"))
		}
		return nil
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		return herror{errors.Wrap(err, "unexpected content type"), http.StatusBadGateway}
	}
	obj := make(map[string]any)
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return herror{errors.Wrap(err, "parsing response"), http.StatusBadGateway}
	}
	// NOTE: Only apply warping if the JSON looks like a non-error response.
	if platform == "npm" && obj["_id"] != nil {
		if err := timeWarpNPMPackageRequest(obj, *t); err != nil {
			return herror{errors.Wrap(err, "warping response"), http.StatusBadGateway}
		}
	} else if platform == "pypi" && obj["releases"] != nil {
		if err := timeWarpPyPIProjectRequest(h.Client, obj, *t); err != nil {
			return herror{errors.Wrap(err, "warping response"), http.StatusBadGateway}
		}
	}
	if err := json.NewEncoder(rw).Encode(obj); err != nil {
		return herror{errors.Wrap(err, "serializing response"), http.StatusBadGateway}
	}
	return nil
}

// timeWarpNPMPackageRequest modifies the provided JSON-like map to exclude all content after "at".
func timeWarpNPMPackageRequest(obj map[string]any, at time.Time) error {
	var futureVersions []string
	var latestVersion string
	var latestVersionTime time.Time
	{
		// Find and exclude versions published after "at"
		times, ok := obj["time"].(map[string]any)
		if !ok {
			return errors.New("unexpected response")
		}
		for tag, ts := range times {
			// Time metadata in RFC3339 the following format.
			// Example: "2020-12-09T15:36:20.909Z"
			t, err := time.Parse(time.RFC3339, ts.(string))
			if err != nil {
				return errors.Wrap(err, "parsing time")
			}
			switch tag {
			case "created":
				if t.After(at) {
					// Fail if the package was created in the future.
					return errors.New("created after time warp")
				}
			case "modified":
				// Will update this value at the end.
			default:
				if t.After(at) {
					futureVersions = append(futureVersions, tag)
				} else if t.After(latestVersionTime) {
					latestVersion = tag
					latestVersionTime = t
				}
			}
		}
		slices.Sort(futureVersions)
		for _, v := range futureVersions {
			delete(times, v)
		}
		times["modified"] = latestVersionTime.Format(time.RFC3339)
	}
	var latestVersionRepo any
	var latestVersionDescription string
	{
		// Find and exclude versions published after "at".
		versions, ok := obj["versions"].(map[string]any)
		if !ok {
			return errors.New("unexpected response")
		}
		for v, val := range versions {
			if v == latestVersion {
				// Record version-specific values present in the top-level response.
				version, ok := val.(map[string]any)
				if !ok {
					return errors.New("unexpected response")
				}
				latestVersionRepo = version["repository"]
				if d, ok := version["description"].(string); ok {
					latestVersionDescription = d
				}
			} else if _, found := slices.BinarySearch(futureVersions, v); found {
				delete(versions, v)
			}
		}
		obj["versions"] = versions
	}
	obj["repository"] = latestVersionRepo
	obj["description"] = latestVersionDescription
	obj["dist-tags"] = map[string]string{"latest": latestVersion}
	return nil
}

// timeWarpPyPIProjectRequest modifies the provided JSON-like map to exclude all content after "at".
func timeWarpPyPIProjectRequest(client httpx.BasicClient, obj map[string]any, at time.Time) error {
	var futureVersions []string
	var latestVersion string
	var latestVersionTime time.Time
	{
		// Find and exclude versions published after "at"
		releases, ok := obj["releases"].(map[string]any)
		if !ok {
			return errors.New("unexpected response")
		}
		for tag, files := range releases {
			var pastFiles []any
			var firstSeen time.Time
			for _, file := range files.([]any) {
				// Time metadata in RFC3339 the following format.
				// Example: "2020-12-09T15:36:20.909808Z"
				uploadedVal, ok := file.(map[string]any)["upload_time_iso_8601"]
				if !ok {
					continue
				}
				uploaded, ok := uploadedVal.(string)
				if !ok {
					continue
				}
				t, err := time.Parse(time.RFC3339, uploaded)
				if err != nil {
					return errors.Wrap(err, "parsing time")
				}
				// NOTE: Ensure that if "at" and "t" are equal, we include the file.
				if t.Before(at.Add(time.Second)) {
					pastFiles = append(pastFiles, file)
				}
				if t.Before(firstSeen) || firstSeen.IsZero() {
					firstSeen = t
				}
			}
			if len(pastFiles) == 0 {
				futureVersions = append(futureVersions, tag)
			} else if firstSeen.After(latestVersionTime) {
				latestVersion = tag
				latestVersionTime = firstSeen
			}
			releases[tag] = pastFiles
		}
		for _, v := range futureVersions {
			delete(releases, v)
		}
	}
	{
		// Merge in data from a version-specific request for the latestVersion.
		// This API is a subset of the project API and the copy in the project
		// response must reflect that of the latest project version.
		//
		// NOTE: For "urls" and "info" (notably "info.requires_dist") to be
		// updated, we need to make this additional request to pypi. These fields
		// are actively used by package manager clients for dependency resolution
		// so we need to make sure it's kept up to date.
		project := obj["info"].(map[string]any)["name"].(string)
		versionURL := pypiRegistry.JoinPath("pypi", project, latestVersion, "json")
		req, err := http.NewRequest(http.MethodGet, versionURL.String(), nil)
		if err != nil {
			return errors.Wrap(err, "creating request")
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode != 200 {
			err = errors.New(resp.Status)
		}
		if err != nil {
			return errors.Wrap(err, "fetching version")
		}
		if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
			return errors.Wrap(err, "decoding version")
		}
	}
	return nil
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
	if err := addFSToTar(tarWriter, wfs); err != nil {
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

// addFSToTar write the fs to the tar archive.
// TODO: Could replace with TarWriter.AddFS once billy is compatible with io.FS
func addFSToTar(tarWriter *tar.Writer, f billy.Filesystem) error {
	return util.Walk(f, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return tarWriter.WriteHeader(&tar.Header{
				Name:     path,
				Mode:     0755,
				Typeflag: tar.TypeDir,
			})
		}
		file, err := f.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: path,
			Mode: 0644,
			Size: info.Size(),
		}); err != nil {
			return err
		}
		_, err = io.Copy(tarWriter, file)
		return err
	})
}
