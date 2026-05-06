// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// handleNPM handles time-warped requests for the NPM registry.
func (h Handler) handleNPM(rw http.ResponseWriter, r *http.Request, t *time.Time) error {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	switch {
	// Reference: https://github.com/npm/registry/blob/master/docs/REGISTRY-API.md
	case len(parts) == 1 && parts[0] != "": // /{pkg}
	case len(parts) == 2 && strings.HasPrefix(parts[0], "@"): // /@{org}/{pkg}
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
	if contentType != "application/json" {
		return herror{errors.New("unexpected content type"), http.StatusBadGateway}
	}
	obj := make(map[string]any)
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return herror{errors.Wrap(err, "parsing response"), http.StatusBadGateway}
	}
	if obj["_id"] != nil {
		if err := timeWarpNPMPackageRequest(obj, *t); err != nil {
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
