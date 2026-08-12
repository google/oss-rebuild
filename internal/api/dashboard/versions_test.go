// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

func TestSortVersionsDesc(t *testing.T) {
	// 5.2.4 is the backport case. Publish time could float it to the top, but
	// semver ordering must keep it below the newer majors.
	got := []string{"5.2.4", "8.18.0", "7.5.13", "6.2.3", "8.5.0", "weird-tag"}
	sortVersionsDesc(got)
	want := []string{"8.18.0", "8.5.0", "7.5.13", "6.2.3", "5.2.4", "weird-tag"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortVersionsDesc = %v, want %v", got, want)
	}
}

func TestPublishedVersionsUnsupported(t *testing.T) {
	// The mux is never consulted. Short-circuiting before any request is what
	// keeps the sentinel distinguishable from a lookup failure.
	mux := meta.NewRegistryMux(&httpxtest.MockClient{})
	_, err := publishedVersions(context.Background(), mux, rebuild.Target{Ecosystem: rebuild.Debian, Package: "curl"})
	if !errors.Is(err, errNoVersionLister) {
		t.Errorf("publishedVersions(debian) error = %v, want errNoVersionLister", err)
	}
}
