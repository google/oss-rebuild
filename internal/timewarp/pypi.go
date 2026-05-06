// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

// handlePyPI handles time-warped requests for the PyPI registry.
func (h Handler) handlePyPI(rw http.ResponseWriter, r *http.Request, t *time.Time) error {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	switch {
	// Reference: https://warehouse.pypa.io/api-reference/json.html
	case len(parts) == 3 && parts[0] == "pypi" && parts[2] == "json": // /pypi/{pkg}/json
	case len(parts) == 2 && parts[0] == "simple": // /simple/{pkg}/ (path.Clean removes trailing slash)
	default:
		http.Redirect(rw, r, r.URL.String(), http.StatusFound)
		return nil
	}
	nr, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
	nr.Header = r.Header.Clone()
	// Remove the basic auth header set with the timewarp params.
	nr.Header.Del("Authorization")
	// Let the HTTP client negotiate encoding rather than forwarding the upstream caller's preference.
	nr.Header.Del("Accept-Encoding")
	if a := nr.Header.Get("Accept"); strings.Contains(a, "application/vnd.pypi.simple.v1+html") {
		if !strings.Contains(a, "application/vnd.pypi.simple.v1+json") {
			return herror{errors.Errorf("unsupported Accept header: %s", a), http.StatusBadGateway}
		}
		nr.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	}
	resp, err := h.Client.Do(nr)
	if err != nil {
		return herror{errors.Wrap(err, "creating client"), http.StatusBadGateway}
	}
	defer resp.Body.Close()
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
	contentType := resp.Header.Get("Content-Type")
	if contentType != "application/json" && contentType != "application/vnd.pypi.simple.v1+json" {
		return herror{errors.New("unexpected content type"), http.StatusBadGateway}
	}
	obj := make(map[string]any)
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return herror{errors.Wrap(err, "parsing response"), http.StatusBadGateway}
	}
	if obj["releases"] != nil {
		if err := timeWarpPyPIProjectRequest(h.Client, obj, *t); err != nil {
			return herror{errors.Wrap(err, "warping response"), http.StatusBadGateway}
		}
	} else if obj["files"] != nil {
		if err := timeWarpPyPISimpleRequest(obj, *t); err != nil {
			return herror{errors.Wrap(err, "warping response"), http.StatusBadGateway}
		}
	}
	if err := json.NewEncoder(rw).Encode(obj); err != nil {
		return herror{errors.Wrap(err, "serializing response"), http.StatusBadGateway}
	}
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
		versionObj := make(map[string]any)
		if err := json.NewDecoder(resp.Body).Decode(&versionObj); err != nil {
			return errors.Wrap(err, "decoding version")
		}
		// Only update the "info" field, preserving the already-processed "releases"
		obj["info"] = versionObj["info"]
	}
	return nil
}

// timeWarpPyPISimpleRequest modifies a PyPI Simple API JSON map to exclude all content after "at".
// It filters the "files" list and then filters the "versions" list to only
// include versions that still have at least one valid file.
func timeWarpPyPISimpleRequest(obj map[string]any, at time.Time) error {
	files, ok := obj["files"].([]any)
	if !ok {
		return errors.New("unexpected response: 'files' key not found or not an array")
	}
	var pastFiles []any
	var pastVersions = make(map[string]struct{})
	for _, fileAny := range files {
		file, ok := fileAny.(map[string]any)
		if !ok {
			continue
		}
		uploadTimeStr, ok := file["upload-time"].(string)
		if !ok {
			continue
		}
		uploadTime, err := time.Parse(time.RFC3339Nano, uploadTimeStr)
		if err != nil {
			return errors.Wrapf(err, "parsing upload-time: %s", uploadTimeStr)
		}
		if uploadTime.After(at) {
			continue // File was uploaded in the future
		}
		// This file is valid for the timewarp so track its version.
		pastFiles = append(pastFiles, file)
		filename, ok := file["filename"].(string)
		if !ok {
			continue
		}
		version := extractVersionFromPyFilename(filename)
		if version != "" {
			pastVersions[version] = struct{}{}
		}
	}
	obj["files"] = pastFiles
	// Filter the versions list based on those found in the past.
	originalVersions, ok := obj["versions"].([]any)
	if !ok {
		return errors.New("unexpected response: 'versions' key not found or not an array")
	}
	var fixedVersions []string
	for _, vAny := range originalVersions {
		vStr, ok := vAny.(string)
		if !ok {
			continue
		}
		if _, exists := pastVersions[vStr]; exists {
			fixedVersions = append(fixedVersions, vStr)
		}
	}
	obj["versions"] = fixedVersions
	return nil
}

// extractVersionFromPyFilename parses a wheel or sdist filename to find its version.
// This is a simplified parser inspired by Python's packaging.utils.
func extractVersionFromPyFilename(filename string) string {
	if strings.HasSuffix(filename, ".whl") {
		// For wheels (e.g., requests-2.31.0-py3-none-any.whl)
		parts := strings.Split(filename, "-")
		if len(parts) >= 5 {
			return parts[1]
		}
	}
	// For sdists (e.g., requests-0.2.0.tar.gz)
	exts := []string{".tar.gz", ".tar.bz2", ".tar.xz", ".zip", ".tar"}
	for _, ext := range exts {
		if strings.HasSuffix(filename, ext) {
			base := strings.TrimSuffix(filename, ext)
			if idx := strings.LastIndex(base, "-"); idx != -1 {
				return base[idx+1:]
			}
		}
	}
	return "" // Unknown format
}
