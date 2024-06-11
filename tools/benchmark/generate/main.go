// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package main generates rebuild benchmark files from external data sources.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/tools/benchmark"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

var (
	outputDir = flag.String("output-dir", "", "directory to which generated files should be written")
	project   = flag.String("project", bigquery.DetectProjectID, "if provided, the project to use to run bigquery jobs")
	only      = flag.String("only", "", "if provided, the only benchmark to generate")
)

// A RebuildBenchmark is a file associated with a PackageSet.
type RebuildBenchmark struct {
	Filename  string
	Generator func(context.Context) benchmark.PackageSet
}

var all = []RebuildBenchmark{
	cratesioTop2000,
	pypiTop250Pure,
	pypiTop1250Pure,
	npmTop500,
	npmTop2500,
	mavenTop500,
}

const (
	maxPackages = 2000
	maxAge      = 5 * (365 * (24 * time.Hour))
)

var cratesioTop2000 = RebuildBenchmark{
	Filename: "cratesio_top_2000.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		client := http.DefaultClient
		now := time.Now()
		ageThreshold := now.Add(-1 * maxAge)
		crates := make(chan cratesio.Metadata, 100)
		// Get download-ordered crates from crates.io.
		go func() {
			for page := 1; len(ps.Packages) < maxPackages; page++ {
				url := fmt.Sprintf("https://crates.io/api/v1/crates?page=%d&per_page=100&sort=downloads", page)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					log.Fatalf("error creating request for download-ordered page %d: %v", page, err)
				}
				resp, err := client.Do(req)
				if err != nil {
					log.Fatalf("error fetching download-ordered page %d: %v", page, err)
				}
				if resp.StatusCode != 200 {
					log.Fatalf("error from regsitry fetching download-ordered page %d: %s", page, resp.Status)
				}
				var ms struct {
					Metadata []cratesio.Metadata `json:"crates"`
				}
				if derr := json.NewDecoder(resp.Body).Decode(&ms); derr != nil {
					log.Fatalf("decoding error on download-ordered page %d: %v", page, err)
				}
				for _, m := range ms.Metadata {
					crates <- m
				}
			}
		}()
		// Select crates with versions that satisfy our criteria.
		for m := range crates {
			if len(ps.Packages) >= maxPackages {
				break
			}
			pmeta, err := cratesio.HTTPRegistry{Client: http.DefaultClient}.Crate(ctx, m.Name)
			if err != nil {
				log.Fatalf("error fetching package metadata for %s: %v", m.Name, err)
			}
			var versions []string
			for _, v := range pmeta.Versions {
				if len(versions) >= 5 {
					break
				}
				isTooOld := v.Created.Before(ageThreshold)
				// NOTE: Assuming versions are valid SemVer, hyphen detects prerelease.
				isPrerelease := strings.ContainsRune(v.Version, '-')
				if v.Yanked || isPrerelease || isTooOld {
					continue
				}
				versions = append(versions, v.Version)
			}
			if len(versions) == 0 {
				log.Printf("No valid candidate versions for pkg %s", m.Name)
				continue
			}
			ps.Count += len(versions)
			pkg := benchmark.Package{Name: m.Name, Ecosystem: "cratesio", Versions: versions}
			ps.Packages = append(ps.Packages, pkg)
			if len(ps.Packages)%500 == 0 {
				log.Printf("Added %d out of %d", len(ps.Packages), maxPackages)
			}
		}
		ps.Updated = now
		return
	},
}

var pypiTop250Pure = RebuildBenchmark{
	Filename: "pypi_top_250_pure.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		// Calculate last Wednesday.
		// Rationale: Wednesday is the least likely day of the week to be a holiday
		// and has the most actual user traffic to PyPI (versus, say, CI).
		lastWednesday := now.AddDate(0, 0, -1)
		for ; lastWednesday.Weekday() != time.Wednesday; lastWednesday = lastWednesday.AddDate(0, 0, -1) {
		}
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
SELECT
  COUNT(*) AS Downloads,
  file.project as Project,
  file.version as Version,
  file.filename as Filename
FROM
  ` + "`" + `bigquery-public-data.pypi.file_downloads` + "`" + `
WHERE
  TIMESTAMP_TRUNC(timestamp, DAY) = TIMESTAMP("` + lastWednesday.Format(time.DateOnly) + `")
GROUP BY
  file.project, file.version, file.filename
ORDER BY
  Downloads DESC
LIMIT 1500
`)
		pkgs := make(chan struct {
			Downloads int64
			Project   string
			Version   string
			Filename  string
		}, 100)
		// Get download-ordered package versions from PyPI's download table.
		go func() {
			j, err := query.Run(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			s, err := j.Wait(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			if s.Err() != nil {
				log.Fatal(s.Err().Error())
			}
			it, err := j.Read(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			var entry struct {
				Downloads int64
				Project   string
				Version   string
				Filename  string
			}
			for {
				err := it.Next(&entry)
				if err == iterator.Done {
					break
				}
				if err != nil {
					log.Fatal(err.Error())
				}
				pkgs <- entry
			}
			close(pkgs)
		}()
		// Select packages with versions that satisfy our criteria.
		for p := range pkgs {
			if strings.ContainsRune(p.Version, '-') {
				// Non-release version.
				continue
			}
			if !(strings.HasSuffix(p.Filename, "none-any.whl") || strings.HasSuffix(p.Filename, ".zip")) {
				// Not a pure python wheel or source archive.
				continue
			}
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == p.Project {
					idx = i
					break
				}
			}
			if idx == -1 {
				if len(ps.Packages) >= 250 {
					// If we're already at the max project count, skip.
					continue
				}
				ps.Packages = append(ps.Packages, benchmark.Package{Name: p.Project, Ecosystem: "pypi"})
				idx = len(ps.Packages) - 1
			}
			psp := &ps.Packages[idx]
			if len(psp.Versions) >= 5 {
				continue
			}
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var pypiTop1250Pure = RebuildBenchmark{
	Filename: "pypi_top_1250_pure.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		// Calculate last Wednesday.
		// Rationale: Wednesday is the least likely day of the week to be a holiday
		// and has the most actual user traffic to PyPI (versus, say, CI).
		lastWednesday := now.AddDate(0, 0, -1)
		for ; lastWednesday.Weekday() != time.Wednesday; lastWednesday = lastWednesday.AddDate(0, 0, -1) {
		}
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
SELECT
  COUNT(*) AS Downloads,
  file.project as Project,
  file.version as Version,
  file.filename as Filename
FROM
  ` + "`" + `bigquery-public-data.pypi.file_downloads` + "`" + `
WHERE
  TIMESTAMP_TRUNC(timestamp, DAY) = TIMESTAMP("` + lastWednesday.Format(time.DateOnly) + `")
GROUP BY
  file.project, file.version, file.filename
ORDER BY
  Downloads DESC
LIMIT 150000
`)
		pkgs := make(chan struct {
			Downloads int64
			Project   string
			Version   string
			Filename  string
		}, 100)
		// Get download-ordered package versions from PyPI's download table.
		go func() {
			j, err := query.Run(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			s, err := j.Wait(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			if s.Err() != nil {
				log.Fatal(s.Err().Error())
			}
			it, err := j.Read(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			var entry struct {
				Downloads int64
				Project   string
				Version   string
				Filename  string
			}
			for {
				err := it.Next(&entry)
				if err == iterator.Done {
					break
				}
				if err != nil {
					log.Fatal(err.Error())
				}
				pkgs <- entry
			}
			close(pkgs)
		}()
		// Select packages with versions that satisfy our criteria.
		for p := range pkgs {
			if strings.ContainsRune(p.Version, '-') {
				// Non-release version.
				continue
			}
			if !(strings.HasSuffix(p.Filename, "none-any.whl") || strings.HasSuffix(p.Filename, ".zip")) {
				// Not a pure python wheel or source archive.
				continue
			}
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == p.Project {
					idx = i
					break
				}
			}
			if idx == -1 {
				if len(ps.Packages) >= 1250 {
					// If we're already at the max project count, skip.
					continue
				}
				ps.Packages = append(ps.Packages, benchmark.Package{Name: p.Project, Ecosystem: "pypi"})
				idx = len(ps.Packages) - 1
			}
			psp := &ps.Packages[idx]
			if len(psp.Versions) >= 2 {
				continue
			}
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var npmTop500 = RebuildBenchmark{
	Filename: "npm_top_500.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
SELECT
  COUNT(*) AS Downloads,
  Name AS Package,
  Version
FROM (
  SELECT
    T.` + "`" + `From` + "`" + `.Name AS FName,
    T.` + "`" + `From` + "`" + `.Version AS FVersion,
    T.` + "`" + `To` + "`" + `.Name AS Name,
    T.` + "`" + `To` + "`" + `.Version AS Version
  FROM
    ` + "`" + `bigquery-public-data.deps_dev_v1.DependencyGraphEdges` + "`" + ` T
  INNER JOIN (
    SELECT
      Time
    FROM
      ` + "`" + `bigquery-public-data.deps_dev_v1.Snapshots` + "`" + `
    ORDER BY
      Time DESC
    LIMIT
      1) S
  ON
    S.Time = T.SnapshotAt
  WHERE
    T.System = "NPM"
  GROUP BY
    T.` + "`" + `From` + "`" + `.Name,
    T.` + "`" + `From` + "`" + `.Version,
    T.` + "`" + `To` + "`" + `.Name,
    T.` + "`" + `To` + "`" + `.Version)
GROUP BY
  Name,
  Version
ORDER BY
  Downloads DESC
LIMIT 2500
`)
		pkgs := make(chan struct {
			Downloads int64
			Package   string
			Version   string
		}, 100)
		// Get download-ordered package versions from deps.dev's dependency table.
		go func() {
			j, err := query.Run(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			s, err := j.Wait(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			if s.Err() != nil {
				log.Fatal(s.Err().Error())
			}
			it, err := j.Read(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			var entry struct {
				Downloads int64
				Package   string
				Version   string
			}
			for {
				err := it.Next(&entry)
				if err == iterator.Done {
					break
				}
				if err != nil {
					log.Fatal(err.Error())
				}
				pkgs <- entry
			}
			close(pkgs)
		}()
		// Select packages with versions that satisfy our criteria.
		for p := range pkgs {
			if strings.ContainsRune(p.Version, '-') {
				// Non-release version.
				continue
			}
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == p.Package {
					idx = i
					break
				}
			}
			if idx == -1 {
				if len(ps.Packages) >= 500 {
					// If we're already at the max project count, skip.
					continue
				}
				ps.Packages = append(ps.Packages, benchmark.Package{Name: p.Package, Ecosystem: "npm"})
				idx = len(ps.Packages) - 1
			}
			psp := &ps.Packages[idx]
			if len(psp.Versions) >= 5 {
				continue
			}
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var npmTop2500 = RebuildBenchmark{
	Filename: "npm_top_2500.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
SELECT
  COUNT(*) AS Downloads,
  Name AS Package,
  Version
FROM (
  SELECT
    T.` + "`" + `From` + "`" + `.Name AS FName,
    T.` + "`" + `From` + "`" + `.Version AS FVersion,
    T.` + "`" + `To` + "`" + `.Name AS Name,
    T.` + "`" + `To` + "`" + `.Version AS Version
  FROM
    ` + "`" + `bigquery-public-data.deps_dev_v1.DependencyGraphEdges` + "`" + ` T
  INNER JOIN (
    SELECT
      Time
    FROM
      ` + "`" + `bigquery-public-data.deps_dev_v1.Snapshots` + "`" + `
    ORDER BY
      Time DESC
    LIMIT
      1) S
  ON
    S.Time = T.SnapshotAt
  WHERE
    T.System = "NPM"
  GROUP BY
    T.` + "`" + `From` + "`" + `.Name,
    T.` + "`" + `From` + "`" + `.Version,
    T.` + "`" + `To` + "`" + `.Name,
    T.` + "`" + `To` + "`" + `.Version)
GROUP BY
  Name,
  Version
ORDER BY
  Downloads DESC
LIMIT 10000
`)
		pkgs := make(chan struct {
			Downloads int64
			Package   string
			Version   string
		}, 100)
		// Get download-ordered package versions from deps.dev's dependency table.
		go func() {
			j, err := query.Run(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			s, err := j.Wait(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			if s.Err() != nil {
				log.Fatal(s.Err().Error())
			}
			it, err := j.Read(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			var entry struct {
				Downloads int64
				Package   string
				Version   string
			}
			for {
				err := it.Next(&entry)
				if err == iterator.Done {
					break
				}
				if err != nil {
					log.Fatal(err.Error())
				}
				pkgs <- entry
			}
			close(pkgs)
		}()
		// Select packages with versions that satisfy our criteria.
		for p := range pkgs {
			if strings.ContainsRune(p.Version, '-') {
				// Non-release version.
				continue
			}
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == p.Package {
					idx = i
					break
				}
			}
			if idx == -1 {
				if len(ps.Packages) >= 2500 {
					// If we're already at the max project count, skip.
					continue
				}
				ps.Packages = append(ps.Packages, benchmark.Package{Name: p.Package, Ecosystem: "npm"})
				idx = len(ps.Packages) - 1
			}
			psp := &ps.Packages[idx]
			if len(psp.Versions) >= 5 {
				continue
			}
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var mavenTop500 = RebuildBenchmark{
	Filename: "maven_top_500.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
SELECT
  COUNT(*) AS Downloads,
  Name AS Package,
  Version
FROM (
  SELECT
    T.` + "`" + `From` + "`" + `.Name AS FName,
    T.` + "`" + `From` + "`" + `.Version AS FVersion,
    T.` + "`" + `To` + "`" + `.Name AS Name,
    T.` + "`" + `To` + "`" + `.Version AS Version
  FROM
    ` + "`" + `bigquery-public-data.deps_dev_v1.DependencyGraphEdges` + "`" + ` T
  INNER JOIN (
    SELECT
      Time
    FROM
      ` + "`" + `bigquery-public-data.deps_dev_v1.Snapshots` + "`" + `
    ORDER BY
      Time DESC
    LIMIT
      1) S
  ON
    S.Time = T.SnapshotAt
  WHERE
    T.System = "MAVEN"
  GROUP BY
    T.` + "`" + `From` + "`" + `.Name,
    T.` + "`" + `From` + "`" + `.Version,
    T.` + "`" + `To` + "`" + `.Name,
    T.` + "`" + `To` + "`" + `.Version)
GROUP BY
  Name,
  Version
ORDER BY
  Downloads DESC
LIMIT 2500
`)
		pkgs := make(chan struct {
			Downloads int64
			Package   string
			Version   string
		}, 100)
		// Get download-ordered package versions from deps.dev's dependency table.
		go func() {
			j, err := query.Run(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			s, err := j.Wait(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			if s.Err() != nil {
				log.Fatal(s.Err().Error())
			}
			it, err := j.Read(ctx)
			if err != nil {
				log.Fatal(err.Error())
			}
			var entry struct {
				Downloads int64
				Package   string
				Version   string
			}
			for {
				err := it.Next(&entry)
				if err == iterator.Done {
					break
				}
				if err != nil {
					log.Fatal(err.Error())
				}
				pkgs <- entry
			}
			close(pkgs)
		}()
		// Select packages with versions that satisfy our criteria.
		for p := range pkgs {
			if strings.ContainsRune(p.Version, '-') {
				// Non-release version.
				continue
			}
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == p.Package {
					idx = i
					break
				}
			}
			if idx == -1 {
				if len(ps.Packages) >= 500 {
					// If we're already at the max project count, skip.
					continue
				}
				ps.Packages = append(ps.Packages, benchmark.Package{Name: p.Package, Ecosystem: "maven"})
				idx = len(ps.Packages) - 1
			}
			psp := &ps.Packages[idx]
			if len(psp.Versions) >= 5 {
				continue
			}
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

func main() {
	flag.Parse()
	ctx := context.Background()
	todo := make(chan any, len(all))
	done := make(chan any)
	for _, b := range all {
		if *only != "" && *only != b.Filename {
			log.Printf("Skipping %s", b.Filename)
			continue
		}
		log.Printf("Generating %s...", b.Filename)
		todo <- nil
		go func(b *RebuildBenchmark) {
			ps := b.Generator(ctx)
			out, err := json.MarshalIndent(ps, "", "  ")
			if err != nil {
				log.Fatalf("error marshalling PackageSet for %s: %v", b.Filename, err)
			}
			path := filepath.Join(*outputDir, b.Filename)
			if err := os.WriteFile(path, out, 0664); err != nil {
				log.Fatalf("error writing %s: %v", b.Filename, err)
			}
			done <- nil
		}(&b)
	}
	close(todo)
	for range todo {
		<-done
	}
	return
}
