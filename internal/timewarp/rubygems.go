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

	"github.com/pkg/errors"
)

// handleRubyGems handles time-warped requests for the RubyGems registry.
func (h Handler) handleRubyGems(rw http.ResponseWriter, r *http.Request, t *time.Time) error {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	switch {
	// Reference: https://guides.rubygems.org/rubygems-org-compact-index-api/
	case len(parts) == 2 && parts[0] == "info": // /info/{gem_name}
	default:
		// All non-compact-index rubygems paths (specs, versions, gem
		// downloads, etc.) are proxied to upstream unfiltered and the
		// subsequent version resolution will use the compact index
		// /info/{gem} path above which IS time-filtered.
		// NOTE: These requests must be proxied rather than redirected to
		// upstream because a redirect causes the gem client to switch to
		// the upstream host for all subsequent requests, bypassing
		// timewarp, which breaks time-filtering.
		return h.proxyUpstream(rw, r)
	}
	nr, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
	nr.Header = r.Header.Clone()
	// Remove the basic auth header set with the timewarp params.
	nr.Header.Del("Authorization")
	// Let the HTTP client negotiate encoding rather than forwarding the upstream caller's preference.
	nr.Header.Del("Accept-Encoding")
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return herror{errors.Wrap(err, "reading response"), http.StatusBadGateway}
	}
	gemName := parts[len(parts)-1]
	filtered, err := h.timeWarpRubyGemsCompactIndexRequest(gemName, string(body), *t)
	if err != nil {
		return herror{errors.Wrap(err, "warping response"), http.StatusBadGateway}
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(rw, filtered); err != nil {
		return herror{errors.Wrap(err, "writing response"), http.StatusBadGateway}
	}
	return nil
}

// proxyUpstream forwards a request to the upstream registry without time filtering.
func (h Handler) proxyUpstream(rw http.ResponseWriter, r *http.Request) error {
	nr, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
	nr.Header = r.Header.Clone()
	// Remove the basic auth header set with the timewarp params.
	nr.Header.Del("Authorization")
	// Let the HTTP client negotiate encoding rather than forwarding the upstream caller's preference.
	nr.Header.Del("Accept-Encoding")
	resp, err := h.Client.Do(nr)
	if err != nil {
		return herror{errors.Wrap(err, "proxying upstream"), http.StatusBadGateway}
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
	rw.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(rw, resp.Body); err != nil {
		log.Printf("error proxying upstream: %v", err)
	}
	return nil
}

// timeWarpRubyGemsCompactIndexRequest filters a RubyGems compact index response to
// exclude versions published after "at". The compact index format has one line per
// version: "VERSION DEPS|checksum:SHA,ruby:CONSTRAINT". Since the compact index
// doesn't include timestamps, we fetch them from the versions API.
func (h Handler) timeWarpRubyGemsCompactIndexRequest(gemName, body string, at time.Time) (string, error) {
	// Fetch version timestamps from the versions API.
	versionsURL := rubygemsRegistry.JoinPath("api/v1/versions", gemName+".json")
	req, err := http.NewRequest(http.MethodGet, versionsURL.String(), nil)
	if err != nil {
		return "", errors.Wrap(err, "creating versions request")
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "fetching versions")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.Errorf("versions API returned %d", resp.StatusCode)
	}
	var versions []struct {
		Number    string    `json:"number"`
		Platform  string    `json:"platform"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", errors.Wrap(err, "parsing versions")
	}
	// Build a set of versions created before the cutoff time.
	// Use version+platform as key since the compact index includes platform variants.
	type versionKey struct {
		number   string
		platform string
	}
	pastVersions := make(map[versionKey]bool)
	for _, v := range versions {
		if !v.CreatedAt.After(at) {
			pastVersions[versionKey{v.Number, v.Platform}] = true
			// Also allow "ruby" platform (the default) when platform matches.
			if v.Platform == "ruby" {
				pastVersions[versionKey{v.Number, ""}] = true
			}
		}
	}
	// Filter the compact index line by line.
	var filtered strings.Builder
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for _, line := range lines {
		// Preserve the header line.
		if line == "---" {
			filtered.WriteString(line)
			filtered.WriteString("\n")
			continue
		}
		// Parse version from the line. Format: "VERSION DEPS|..." or "VERSION |..."
		version, _, _ := strings.Cut(line, " ")
		if version == "" {
			continue
		}
		// Check if this version is in the past set.
		// The compact index uses "ruby" as the default platform (no platform suffix).
		if pastVersions[versionKey{version, ""}] || pastVersions[versionKey{version, "ruby"}] {
			filtered.WriteString(line)
			filtered.WriteString("\n")
		}
	}
	return filtered.String(), nil
}
