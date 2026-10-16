// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/dump"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func must1(err error) {
	if err != nil {
		panic(err)
	}
}

func must[T any](t T, err error) T {
	must1(err)
	return t
}

const (
	cratesCSV = `id,name,repository,created_at,updated_at
1,serde,https://github.com/serde-rs/serde,2014-12-13 22:10:11+00,2026-09-30 01:02:03+00
`
	versionsCSV = `crate_id,num,repository,rust_version,created_at,updated_at,yanked
1,1.0.150,,1.13,2022-12-12 00:25:28+00,2022-12-12 00:25:28+00,f
`
)

func TestMetadataHandlers(t *testing.T) {
	ctx := t.Context()
	tgz := must(archivetest.TgzFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data/crates.csv"}, Body: []byte(cratesCSV)},
		{Header: &tar.Header{Name: "data/versions.csv"}, Body: []byte(versionsCSV)},
	}))
	loaded := &MetadataDeps{Dump: must(dump.Read(tgz))}
	for _, tc := range []struct {
		name string
		deps *MetadataDeps
		req  VersionRequest
		want codes.Code
	}{
		{name: "Found", deps: loaded, req: VersionRequest{Name: "serde", Version: "1.0.150"}, want: codes.OK},
		{name: "UnknownVersion", deps: loaded, req: VersionRequest{Name: "serde", Version: "9.9.9"}, want: codes.NotFound},
		{name: "NotLoaded", deps: &MetadataDeps{}, req: VersionRequest{Name: "serde", Version: "1.0.150"}, want: codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Version(ctx, tc.req, tc.deps)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("Version() code = %v (%v), want %v", got, err, tc.want)
			}
			if err == nil && (v.Version.Version != "1.0.150" || v.RustVersion != "1.13") {
				t.Errorf("Version() = %+v", v)
			}
			c, err := Crate(ctx, CrateRequest{Name: tc.req.Name}, tc.deps)
			if tc.want == codes.Unavailable {
				if status.Code(err) != codes.Unavailable {
					t.Errorf("Crate() error = %v, want Unavailable", err)
				}
			} else if err != nil || c.Repository != "https://github.com/serde-rs/serde" {
				t.Errorf("Crate() = %+v, %v", c, err)
			}
		})
	}
}

// liveRegistry records the lookups that reach crates.io.
type liveRegistry struct{ calls []string }

func (l *liveRegistry) Crate(_ context.Context, name string) (*cratesio.Crate, error) {
	l.calls = append(l.calls, "crate "+name)
	return &cratesio.Crate{Metadata: cratesio.Metadata{Name: name, Repository: "live"}}, nil
}

func (l *liveRegistry) Version(_ context.Context, name, version string) (*cratesio.CrateVersion, error) {
	l.calls = append(l.calls, "version "+name+"@"+version)
	return &cratesio.CrateVersion{Version: cratesio.Version{Version: version, Repository: "live"}}, nil
}

func (l *liveRegistry) Artifact(_ context.Context, name, version string) (io.ReadCloser, error) {
	l.calls = append(l.calls, "artifact "+name+"@"+version)
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func TestBackedByFallsBackToLive(t *testing.T) {
	ctx := t.Context()
	tgz := must(archivetest.TgzFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data/crates.csv"}, Body: []byte(cratesCSV)},
		{Header: &tar.Header{Name: "data/versions.csv"}, Body: []byte(versionsCSV)},
	}))
	deps := &MetadataDeps{Dump: must(dump.Read(tgz))}
	depsFn := func(context.Context) (*MetadataDeps, error) { return deps, nil }
	mux := http.NewServeMux()
	mux.HandleFunc("/crate", api.Handler(depsFn, Crate))
	mux.HandleFunc("/version", api.Handler(depsFn, Version))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	live := &liveRegistry{}
	r := NewCache(srv.Client(), must(url.Parse(srv.URL))).BackedBy(live)
	if v, err := r.Version(ctx, "serde", "1.0.150"); err != nil || v.Repository == "live" {
		t.Errorf("Version(serde@1.0.150) = %+v, %v; want the dump's record", v, err)
	}
	if v, err := r.Version(ctx, "serde", "1.0.151"); err != nil || v.Repository != "live" {
		t.Errorf("Version(serde@1.0.151) = %+v, %v; want live for a version the dump lacks", v, err)
	}
	if c, err := r.Crate(ctx, "serde"); err != nil || c.Repository == "live" {
		t.Errorf("Crate(serde) = %+v, %v; want the dump's record", c, err)
	}
	must(r.Artifact(ctx, "serde", "1.0.150"))
	// While the dump loads, everything goes live.
	deps.Dump = nil
	if c, err := r.Crate(ctx, "serde"); err != nil || c.Repository != "live" {
		t.Errorf("Crate(serde) before load = %+v, %v; want live", c, err)
	}
	want := []string{"version serde@1.0.151", "artifact serde@1.0.150", "crate serde"}
	if len(live.calls) != len(want) {
		t.Fatalf("live calls = %v, want %v", live.calls, want)
	}
	for i := range want {
		if live.calls[i] != want[i] {
			t.Errorf("live call %d = %q, want %q", i, live.calls[i], want[i])
		}
	}
}
