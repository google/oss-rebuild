// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
)

func TestInferStrategy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		repo     string
		metadata string
		files    []archive.TarEntry
		filesFn  func(*gitxtest.Repository) []archive.TarEntry
		wantFn   func(*gitxtest.Repository) rebuild.Strategy
		wantErr  bool
	}{
		{
			name: "ref from cargo_vcs_info",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: version-bump
    parent: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.150"
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","rust_version": "1.13"}}`,
			filesFn: func(repo *gitxtest.Repository) []archive.TarEntry {
				return []archive.TarEntry{
					{Header: &tar.Header{Name: "serde-1.0.150/.cargo_vcs_info.json"}, Body: []byte(`{"git":{"sha1":"` + repo.Commits["version-bump"].String() + `"}}`)},
				}
			},
			wantFn: func(repo *gitxtest.Repository) rebuild.Strategy {
				return &CratesIOCargoPackage{
					Location: rebuild.Location{
						Repo: "https://github.com/serde-rs/serde",
						Ref:  repo.Commits["version-bump"].String(),
						Dir:  ".",
					},
					RustVersion: "1.13",
				}
			},
		},
		{
			name: "rust_version from updated_at",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: version-bump
    parent: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.150"
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","updated_at":"2022-12-12T00:25:28.357Z"}}`,
			files:    []archive.TarEntry{},
			wantFn: func(repo *gitxtest.Repository) rebuild.Strategy {
				return &CratesIOCargoPackage{
					Location: rebuild.Location{
						Repo: "https://github.com/serde-rs/serde",
						Ref:  repo.Commits["version-bump"].String(),
						Dir:  ".",
					},
					RustVersion: "1.65.0",
				}
			},
		},
		{
			name: "ref from tag",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: tagged-target
    parent: initial-commit
    tag: v1.0.150
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.150"
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","rust_version": "1.13"}}`,
			files:    []archive.TarEntry{},
			wantFn: func(repo *gitxtest.Repository) rebuild.Strategy {
				return &CratesIOCargoPackage{
					Location: rebuild.Location{
						Repo: "https://github.com/serde-rs/serde",
						Ref:  repo.Commits["tagged-target"].String(),
						Dir:  ".",
					},
					RustVersion: "1.13",
				}
			},
		},
		{
			name: "ref from refmap",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: version-bump
    parent: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.150"
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","rust_version": "1.13"}}`,
			files:    []archive.TarEntry{},
			wantFn: func(repo *gitxtest.Repository) rebuild.Strategy {
				return &CratesIOCargoPackage{
					Location: rebuild.Location{
						Repo: "https://github.com/serde-rs/serde",
						Ref:  repo.Commits["version-bump"].String(),
						Dir:  ".",
					},
					RustVersion: "1.13",
				}
			},
		},
		{
			name: "unreadable Cargo.toml",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: version-bump
    parent: initial-commit
    files:
      Cargo.toml: |
        -asd;lkjasd;lkjasd
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","rust_version": "1.13"}}`,
			files:    []archive.TarEntry{},
			wantErr:  true,
		},
		{
			name: "broken cargo_vcs_info",
			repo: `commits:
  - id: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.0"
  - id: version-bump
    parent: initial-commit
    files:
      Cargo.toml: |
        [package]
        name = "serde"
        version = "1.0.150"
`,
			metadata: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download","rust_version": "1.13"}}`,
			files: []archive.TarEntry{
				{Header: &tar.Header{Name: "serde-1.0.150/.cargo_vcs_info.json"}, Body: []byte(`broken json`)},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
			target := rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "serde", Version: "1.0.150", Artifact: "serde-1.0.150.crate"}
			rcfg := rebuild.RepoConfig{
				Repository: repo.Repository,
				URI:        "https://github.com/serde-rs/serde",
				Dir:        ".",
				RefMap:     map[string]string{"1.0.150": repo.Commits["version-bump"].String()},
			}
			files := tc.files
			if tc.filesFn != nil {
				files = tc.filesFn(repo)
			}
			client := httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						URL: "https://crates.io/api/v1/crates/serde/1.0.150",
						Response: &http.Response{
							StatusCode: 200,
							Body:       io.NopCloser(bytes.NewReader([]byte(tc.metadata))),
						},
					},
					{
						URL: "https://crates.io/api/v1/crates/serde/1.0.150",
						Response: &http.Response{
							StatusCode: 200,
							Body:       io.NopCloser(bytes.NewReader([]byte(tc.metadata))),
						},
					},
					{
						URL: "https://crates.io/api/v1/crates/serde/1.0.150/download",
						Response: &http.Response{
							StatusCode: 200,
							Body:       io.NopCloser(must(archivetest.TgzFile(files))),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			mux := rebuild.RegistryMux{CratesIO: cratesio.HTTPRegistry{Client: &client}}
			s, err := Rebuilder{}.InferStrategy(ctx, target, mux, &rcfg, nil)
			if tc.wantErr {
				if err == nil {
					t.Errorf("InferStrategy expected error, got %v", s)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				want := tc.wantFn(repo)
				if diff := cmp.Diff(want, s); diff != "" {
					t.Errorf("InferStrategy mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
