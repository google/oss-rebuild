// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package gcsxtest serves GCS objects from memory for tests.
package gcsxtest

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// Server serves object reads from memory to a real storage client. Objects
// are keyed "bucket/object", and a missing key is a missing object.
type Server struct {
	// Client reads from the server.
	Client *storage.Client

	mu      sync.Mutex
	objects map[string]string
}

// NewServer serves objects until t ends.
func NewServer(t testing.TB, objects map[string]string) *Server {
	t.Helper()
	s := &Server{objects: maps.Clone(objects)}
	if s.objects == nil {
		s.objects = map[string]string{}
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	client, err := storage.NewClient(t.Context(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	s.Client = client
	return s
}

// Client returns a client reading objects from a new Server.
func Client(t testing.TB, objects map[string]string) *storage.Client {
	return NewServer(t, objects).Client
}

// Put adds or replaces an object, for tests whose objects appear as they run.
func (s *Server) Put(key, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = body
}

// ServeHTTP answers byte ranges as GCS does.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "gcsxtest serves object reads only", http.StatusNotImplemented)
		return
	}
	s.mu.Lock()
	body, ok := s.objects[strings.TrimPrefix(r.URL.Path, "/")]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	size := int64(len(body))
	start, end := int64(0), size-1
	spec, ranged := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if ranged {
		first, last, _ := strings.Cut(spec, "-")
		if first == "" {
			n, _ := strconv.ParseInt(last, 10, 64)
			start = max(0, size-n)
		} else {
			start, _ = strconv.ParseInt(first, 10, 64)
			if last != "" {
				l, _ := strconv.ParseInt(last, 10, 64)
				end = min(end, l)
			}
		}
		// NOTE: GCS refuses every range on an empty object, a suffix range
		// included, where RFC 9110 serves the empty body.
		if size == 0 || start >= size {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if ranged {
		w.WriteHeader(http.StatusPartialContent)
	}
	io.WriteString(w, body[start:end+1])
}
