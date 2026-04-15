// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/urlx"
)

func TestHTTPRegistry_Gem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/gems/rails.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(Gem{
			Name:       "rails",
			Version:    "7.1.0",
			SourceCode: "https://github.com/rails/rails",
			Homepage:   "https://rubyonrails.org",
			GemURI:     "https://rubygems.org/gems/rails-7.1.0.gem",
		})
	}))
	defer server.Close()

	// Override registryURL for testing
	origURL := registryURL
	registryURL = urlx.MustParse(server.URL)
	defer func() { registryURL = origURL }()

	reg := HTTPRegistry{Client: http.DefaultClient}
	gem, err := reg.Gem(context.Background(), "rails")
	if err != nil {
		t.Fatalf("Gem() error = %v", err)
	}
	if gem.Name != "rails" {
		t.Errorf("Gem.Name = %q, want %q", gem.Name, "rails")
	}
	if gem.Version != "7.1.0" {
		t.Errorf("Gem.Version = %q, want %q", gem.Version, "7.1.0")
	}
	if gem.SourceCode != "https://github.com/rails/rails" {
		t.Errorf("Gem.SourceCode = %q, want %q", gem.SourceCode, "https://github.com/rails/rails")
	}
}

func TestHTTPRegistry_Versions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/versions/rails.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]VersionInfo{
			{Number: "7.1.0", Platform: "ruby", CreatedAt: time.Now()},
			{Number: "7.0.8", Platform: "ruby", CreatedAt: time.Now()},
		})
	}))
	defer server.Close()

	origURL := registryURL
	registryURL = urlx.MustParse(server.URL)
	defer func() { registryURL = origURL }()

	reg := HTTPRegistry{Client: http.DefaultClient}
	versions, err := reg.Versions(context.Background(), "rails")
	if err != nil {
		t.Fatalf("Versions() error = %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("len(versions) = %d, want 2", len(versions))
	}
	if versions[0].Number != "7.1.0" {
		t.Errorf("versions[0].Number = %q, want %q", versions[0].Number, "7.1.0")
	}
}

func TestHTTPRegistry_Artifact(t *testing.T) {
	gemContent := []byte("fake gem content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gems/rails-7.1.0.gem" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write(gemContent)
	}))
	defer server.Close()

	origURL := registryURL
	registryURL = urlx.MustParse(server.URL)
	defer func() { registryURL = origURL }()

	reg := HTTPRegistry{Client: http.DefaultClient}
	rc, err := reg.Artifact(context.Background(), "rails", "7.1.0")
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(content) != string(gemContent) {
		t.Errorf("content = %q, want %q", content, gemContent)
	}
}

func TestHTTPRegistry_ArtifactURL(t *testing.T) {
	reg := HTTPRegistry{Client: http.DefaultClient}
	url := reg.ArtifactURL("rails", "7.1.0")
	want := "https://rubygems.org/gems/rails-7.1.0.gem"
	if url != want {
		t.Errorf("ArtifactURL() = %q, want %q", url, want)
	}
}
