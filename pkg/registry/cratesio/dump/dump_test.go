// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dump

import (
	"archive/tar"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
)

const (
	cratesCSV = `created_at,description,id,name,readme,repository,updated_at
2014-12-13 22:10:11.303311+00,serialization,463,serde,"# serde
A multi-line readme, with ""quotes"".",https://github.com/serde-rs/serde,2026-09-30 01:02:03+00
2015-05-01 00:00:00+00,derive,1000,serde_derive,,,2026-01-01 00:00:00+00
`
	versionsCSV = `crate_id,created_at,num,repository,rust_version,updated_at,yanked
463,2022-12-12 00:25:28.357+00,1.0.150,https://github.com/serde-rs/serde,1.13,2022-12-12 00:25:28.357+00,f
463,2022-12-13 00:00:00+00,1.0.151,,,2022-12-14 00:00:00+00,t
999,2020-01-01 00:00:00+00,0.1.0,,,2020-01-01 00:00:00+00,f
1000,2015-05-01 00:00:00+00,0.1.0,,,2015-05-01 00:00:00+00,f
`
)

func TestRead(t *testing.T) {
	tgz, err := archivetest.TgzFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "2026-10-16-020026/README.md"}, Body: []byte("notes")},
		{Header: &tar.Header{Name: "2026-10-16-020026/data/crates.csv"}, Body: []byte(cratesCSV)},
		{Header: &tar.Header{Name: "2026-10-16-020026/data/versions.csv"}, Body: []byte(versionsCSV)},
		// Read stops after versions.csv, so this is never parsed.
		{Header: &tar.Header{Name: "2026-10-16-020026/data/dependencies.csv"}, Body: []byte("not,\"csv")},
	})
	if err != nil {
		t.Fatal(err)
	}
	x, err := Read(tgz)
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v150 := cratesio.Version{
		Version:      "1.0.150",
		RustVersion:  "1.13",
		DownloadPath: "/api/v1/crates/serde/1.0.150/download",
		DownloadURL:  "https://crates.io/api/v1/crates/serde/1.0.150/download",
		Repository:   "https://github.com/serde-rs/serde",
		Created:      at("2022-12-12T00:25:28.357Z"),
		Updated:      at("2022-12-12T00:25:28.357Z"),
	}
	v151 := cratesio.Version{
		Version:      "1.0.151",
		DownloadPath: "/api/v1/crates/serde/1.0.151/download",
		DownloadURL:  "https://crates.io/api/v1/crates/serde/1.0.151/download",
		Created:      at("2022-12-13T00:00:00Z"),
		Updated:      at("2022-12-14T00:00:00Z"),
		Yanked:       true,
	}
	// Lookups fold case as crates.io does.
	got, ok := x.Crate("Serde")
	if !ok {
		t.Fatal("Crate(Serde) missing")
	}
	want := &cratesio.Crate{
		Metadata: cratesio.Metadata{Name: "serde", Repository: "https://github.com/serde-rs/serde", Created: at("2014-12-13T22:10:11.303311Z"), Updated: at("2026-09-30T01:02:03Z")},
		Versions: []cratesio.Version{v150, v151},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Crate(Serde) diff (-want +got):\n%s", diff)
	}
	gotV, ok := x.Version("serde", "1.0.151")
	if !ok {
		t.Fatal("Version(serde, 1.0.151) missing")
	}
	if diff := cmp.Diff(&cratesio.CrateVersion{Version: v151}, gotV); diff != "" {
		t.Errorf("Version(serde, 1.0.151) diff (-want +got):\n%s", diff)
	}
	// Dashes and underscores match each other, and the published name wins.
	if c, ok := x.Crate("serde-derive"); !ok || c.Name != "serde_derive" || len(c.Versions) != 1 {
		t.Errorf("Crate(serde-derive) = %+v, %v; want serde_derive with one version", c, ok)
	}
	if _, ok := x.Version("serde", "9.9.9"); ok {
		t.Error("Version(serde, 9.9.9) found")
	}
	if _, ok := x.Crate("unknown"); ok {
		t.Error("Crate(unknown) found")
	}
}

func TestReadNeedsBothTables(t *testing.T) {
	crates := archive.TarEntry{Header: &tar.Header{Name: "data/crates.csv"}, Body: []byte(cratesCSV)}
	versions := archive.TarEntry{Header: &tar.Header{Name: "data/versions.csv"}, Body: []byte(versionsCSV)}
	for name, entries := range map[string][]archive.TarEntry{
		"VersionsFirst": {versions, crates},
		"NoVersions":    {crates},
		"MissingColumn": {{Header: &tar.Header{Name: "data/crates.csv"}, Body: []byte("id,name\n1,a\n")}, versions},
	} {
		t.Run(name, func(t *testing.T) {
			tgz, err := archivetest.TgzFile(entries)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Read(tgz); err == nil {
				t.Error("Read() succeeded, want an error")
			}
		})
	}
}
