// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
)

type staticRegistry struct {
	crate   *reg.Crate
	version *reg.CrateVersion
}

func (r staticRegistry) Crate(context.Context, string) (*reg.Crate, error) {
	return r.crate, nil
}

func (r staticRegistry) Version(context.Context, string, string) (*reg.CrateVersion, error) {
	if r.version == nil {
		panic("unexpected Version call")
	}
	return r.version, nil
}

func (staticRegistry) Artifact(context.Context, string, string) (io.ReadCloser, error) {
	panic("unexpected Artifact call")
}

func TestGetVersionsOmitsOnlyPrereleases(t *testing.T) {
	date := func(day int) time.Time {
		return time.Date(2026, time.January, day, 0, 0, 0, 0, time.UTC)
	}
	mux := rebuild.RegistryMux{CratesIO: staticRegistry{crate: &reg.Crate{
		Versions: []reg.Version{
			{Version: "1.0.0-alpha.1", Created: date(4)},
			{Version: "0.14.7+wasi-0.2.4", Created: date(3)},
			{Version: "1.0.0+build-1", Created: date(2)},
			{Version: "0.9.0", Created: date(1)},
		},
	}}}

	got, err := GetVersions(context.Background(), "example", mux)
	if err != nil {
		t.Fatalf("GetVersions() error = %v", err)
	}
	want := []string{"0.14.7+wasi-0.2.4", "1.0.0+build-1", "0.9.0"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetVersions() mismatch (-want +got):\n%s", diff)
	}
}

func TestInferRepoPrefersVersionRepository(t *testing.T) {
	target := rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "rand_pcg", Version: "0.1.2", Artifact: "rand_pcg-0.1.2.crate"}
	for _, tc := range []struct {
		name, versionRepo, want string
	}{
		{"version names the repository of its release", "https://github.com/rust-random/rand", "https://github.com/rust-random/rand"},
		{"version names none so the crate's is used", "", "https://github.com/rust-random/rngs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := rebuild.RegistryMux{CratesIO: staticRegistry{
				crate:   &reg.Crate{Metadata: reg.Metadata{Repository: "https://github.com/rust-random/rngs"}},
				version: &reg.CrateVersion{Version: reg.Version{Version: "0.1.2", Repository: tc.versionRepo}},
			}}
			got, err := Rebuilder{}.InferRepo(context.Background(), target, mux)
			if err != nil {
				t.Fatalf("InferRepo() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("InferRepo() = %q, want %q", got, tc.want)
			}
		})
	}
}
