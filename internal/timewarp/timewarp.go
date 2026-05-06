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
	"log"
	"net/http"
	"net/url"
	"regexp"
	"syscall"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var (
	npmRegistry         = urlx.MustParse("https://registry.npmjs.org/")
	pypiRegistry        = urlx.MustParse("https://pypi.org/")
	rubygemsRegistry    = urlx.MustParse("https://rubygems.org/")
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

// isClientDisconnect checks if the error is due to client disconnecting (broken pipe or connection reset)
func isClientDisconnect(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

func (h Handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if err := h.handleRequest(rw, r); err != nil {
		if isClientDisconnect(err) {
			return // don't try to write an error response
		}
		status := http.StatusInternalServerError
		if he, ok := err.(herror); ok {
			status = he.status
			if isClientDisconnect(he.error) {
				return
			}
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
	case "rubygems":
		r.URL.Host = rubygemsRegistry.Host
		r.URL.Scheme = rubygemsRegistry.Scheme
	// TODO: We should add cargogit which serves the repo from a given set of packages. This is built into go-git v6.
	case "cargogitarchive":
		return h.handleCargoGitArchive(rw, r, ts)
	case "cargosparse":
		return h.handleCargoSparse(rw, r, ts)
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
	switch platform {
	case "npm":
		return h.handleNPM(rw, r, t)
	case "pypi":
		return h.handlePyPI(rw, r, t)
	case "rubygems":
		return h.handleRubyGems(rw, r, t)
	default:
		return herror{errors.New("unsupported platform"), http.StatusBadRequest}
	}
}
