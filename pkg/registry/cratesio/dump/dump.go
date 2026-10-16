// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package dump serves crates.io crate and version metadata from the daily
// database dump, so lookups cost no API requests.
package dump

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pkg/errors"
)

// URL is the daily dump of the crates.io database.
const URL = "https://static.crates.io/db-dump.tar.gz"

// Index holds the dump's metadata for every crate, by canonical name.
// TODO: Consider SQLite storage, for about half the memory (a third at peak) and to keep serving while the next dump loads.
type Index struct {
	ETag   string // set by Fetch, empty from Read
	crates map[string]*crate
}

type crate struct {
	name, repository string
	created, updated time.Time
	versions         []version
}

type version struct {
	num, rustVersion, repository string
	created, updated             time.Time
	yanked                       bool
}

// canonical folds a crate name the way crates.io matches names.
func canonical(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

// Latest returns the published dump's ETag, which changes daily.
func Latest(ctx context.Context, client httpx.BasicClient) (string, error) {
	resp, err := requestDump(ctx, client, http.MethodHead)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return resp.Header.Get("ETag"), nil
}

// Fetch downloads and reads the current dump.
func Fetch(ctx context.Context, client httpx.BasicClient) (*Index, error) {
	resp, err := requestDump(ctx, client, http.MethodGet)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	x, err := Read(resp.Body)
	if err != nil {
		return nil, err
	}
	x.ETag = resp.Header.Get("ETag")
	return x, nil
}

// requestDump requests the dump with method, failing on any status but 200 OK.
func requestDump(ctx context.Context, client httpx.BasicClient, method string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "fetching the crates.io dump")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errors.Errorf("fetching the crates.io dump: %s", resp.Status)
	}
	return resp, nil
}

// Read reads a gzipped dump archive. It stops after versions.csv, so the
// larger tables behind it are never read.
func Read(r io.Reader) (*Index, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, errors.Wrap(err, "opening the dump")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var x *Index
	var byID map[string]*crate
	for h, err := range iterx.ToSeq2(tr, io.EOF) {
		if err != nil {
			return nil, errors.Wrap(err, "reading the dump")
		}
		switch path.Base(h.Name) {
		case "crates.csv":
			if x, byID, err = readCrates(tr); err != nil {
				return nil, errors.Wrap(err, "reading crates.csv")
			}
		case "versions.csv":
			if x == nil {
				return nil, errors.New("dump has versions.csv before crates.csv")
			}
			if err := readVersions(tr, byID); err != nil {
				return nil, errors.Wrap(err, "reading versions.csv")
			}
			return x, nil
		}
	}
	return nil, errors.New("dump has no versions.csv")
}

// csvRows adapts a csv.Reader to iterx.ToSeq2.
type csvRows struct{ *csv.Reader }

func (r csvRows) Next() ([]string, error) { return r.Read() }

// readTable reads a CSV table with a header row and calls row with each
// record's cols, by column name. The map is reused across rows.
func readTable(r io.Reader, cols []string, row func(map[string]string) error) error {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return err
	}
	idx := map[string]int{}
	for i, name := range header {
		idx[name] = i
	}
	for _, c := range cols {
		if _, ok := idx[c]; !ok {
			return errors.Errorf("no %s column", c)
		}
	}
	fields := map[string]string{}
	for rec, err := range iterx.ToSeq2(csvRows{cr}, io.EOF) {
		if err != nil {
			return err
		}
		for _, c := range cols {
			fields[c] = strings.Clone(rec[idx[c]])
		}
		if err := row(fields); err != nil {
			return err
		}
	}
	return nil
}

func readCrates(r io.Reader) (*Index, map[string]*crate, error) {
	x := &Index{crates: map[string]*crate{}}
	byID := map[string]*crate{}
	err := readTable(r, []string{"id", "name", "repository", "created_at", "updated_at"}, func(f map[string]string) error {
		c := &crate{name: f["name"], repository: f["repository"]}
		var err error
		if c.created, err = parseTime(f["created_at"]); err != nil {
			return err
		}
		if c.updated, err = parseTime(f["updated_at"]); err != nil {
			return err
		}
		byID[f["id"]] = c
		x.crates[canonical(c.name)] = c
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return x, byID, nil
}

func readVersions(r io.Reader, byID map[string]*crate) error {
	return readTable(r, []string{"crate_id", "num", "rust_version", "repository", "created_at", "updated_at", "yanked"}, func(f map[string]string) error {
		c, ok := byID[f["crate_id"]]
		if !ok {
			return nil // crate deleted between table exports
		}
		v := version{num: f["num"], rustVersion: f["rust_version"], repository: f["repository"], yanked: f["yanked"] == "t"}
		var err error
		if v.created, err = parseTime(f["created_at"]); err != nil {
			return err
		}
		if v.updated, err = parseTime(f["updated_at"]); err != nil {
			return err
		}
		c.versions = append(c.versions, v)
		return nil
	})
}

// parseTime reads a PostgreSQL timestamptz as the dump writes it.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05.999999-07", s)
	return t.UTC(), errors.Wrapf(err, "parsing time %q", s)
}

// Crate returns a crate's metadata as the crates.io API serves it.
func (x *Index) Crate(name string) (*cratesio.Crate, bool) {
	c, ok := x.crates[canonical(name)]
	if !ok {
		return nil, false
	}
	out := &cratesio.Crate{Metadata: cratesio.Metadata{Name: c.name, Repository: c.repository, Created: c.created, Updated: c.updated}}
	for _, v := range c.versions {
		out.Versions = append(out.Versions, v.api(c.name))
	}
	return out, true
}

// Version returns a crate version's metadata as the crates.io API serves it.
func (x *Index) Version(name, num string) (*cratesio.CrateVersion, bool) {
	c, ok := x.crates[canonical(name)]
	if !ok {
		return nil, false
	}
	for _, v := range c.versions {
		if v.num == num {
			return &cratesio.CrateVersion{Version: v.api(c.name)}, true
		}
	}
	return nil, false
}

func (v version) api(name string) cratesio.Version {
	return cratesio.Version{
		Version:      v.num,
		RustVersion:  v.rustVersion,
		DownloadPath: path.Join("/api/v1/crates", name, v.num, "download"),
		DownloadURL:  cratesio.DownloadURL(name, v.num),
		Repository:   v.repository,
		Created:      v.created,
		Updated:      v.updated,
		Yanked:       v.yanked,
	}
}
