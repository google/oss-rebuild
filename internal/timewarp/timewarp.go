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
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var (
	npmRegistry  = urlx.MustParse("https://registry.npmjs.org/")
	pypiRegistry = urlx.MustParse("https://pypi.org/")
	lowTimeBound = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
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
