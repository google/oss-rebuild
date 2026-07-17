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
	crate *reg.Crate
}

func (r staticRegistry) Crate(context.Context, string) (*reg.Crate, error) {
	return r.crate, nil
}

func (staticRegistry) Version(context.Context, string, string) (*reg.CrateVersion, error) {
	panic("unexpected Version call")
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
