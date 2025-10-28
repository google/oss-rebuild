// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package main generates rebuild benchmark files from external data sources.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	debianTop500,
	debianTop500Stable,
	pypiTop250Pure,
	pypiTop1250Pure,
	npmTop500,
	npmTop2500,
	mavenTop500,
	mavenRecentTop500,
	mavenRecentAll,
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
					log.Fatalf("error from registry fetching download-ordered page %d: %s", page, resp.Status)
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

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	client := http.DefaultClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.New(resp.Status)
	}
	return resp.Body, nil
}

var debianTop500 = RebuildBenchmark{
	Filename: "debian_top_500.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		var (
			// The "all" arch is included in other arch indices.
			archs         = []string{"amd64"}
			popularityURL = "https://popcon.debian.org/by_inst"
			repositoryURL = "https://deb.debian.org/debian"
		)
		resp, err := get(ctx, popularityURL)
		if err != nil {
			log.Fatalf("error fetching popularity: %v", err)
		}
		b := bufio.NewScanner(resp)
		var popularPackages []string
		// Lines beginning with '#' are comments. Here are the column names and first row:
		// #rank name                            inst  vote   old recent no-files (maintainer)
		// 1     adduser                        248240 227236  4237 16731    36 (Debian Adduser Developers)
		for b.Scan() {
			line := strings.TrimSpace(b.Text())
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			// We only care about the package name.
			popularPackages = append(popularPackages, f[1])
		}
		// Mapping from package name to a set of strings like "component/sourceName/version/artifact" where package name matches the artifact.
		var elementsRegex = regexp.MustCompile(`^(?P<component>[^/]+)\/(?P<sourceName>[^/]+)\/(?P<version>[^/]+)\/(?P<artifact>[^/]+)$`)
		repo := map[string]map[string]bool{}
		for _, arch := range archs {
			index, err := get(ctx, repositoryURL+fmt.Sprintf("/indices/files/arch-%s.files", arch))
			if err != nil {
				log.Fatalf("error fetching arch %s index: %v", arch, err)
			}
			b = bufio.NewScanner(index)
			// Each line in the index is a relative path from the repository root. Files included are *.dsc, *.tar.gz, and *.deb.
			// And example would be:
			// ./pool/contrib/a/alex4/alex4_1.1-10+b2_amd64.deb
			packagePathRegex := regexp.MustCompile(`^\.\/pool\/(?P<component>[^/]+)\/[^/]+\/(?P<sourceName>[^/]+)\/(?P<packageName>[^_]+)_(?P<version>[^_]+)_[^_]+\.deb$`)
			for b.Scan() {
				if matches := packagePathRegex.FindStringSubmatch(strings.TrimSpace(b.Text())); matches != nil {
					component := matches[packagePathRegex.SubexpIndex("component")]
					sourceName := matches[packagePathRegex.SubexpIndex("sourceName")]
					packageName := matches[packagePathRegex.SubexpIndex("packageName")]
					version := matches[packagePathRegex.SubexpIndex("version")]
					artifact := filepath.Base(b.Text())
					if _, ok := repo[packageName]; !ok {
						repo[packageName] = map[string]bool{}
					}
					repo[packageName][fmt.Sprintf("%s/%s/%s/%s", component, sourceName, version, artifact)] = true
				}
			}
		}
		type Artifact struct {
			Version string
			Name    string
		}
		for _, p := range popularPackages {
			if ps.Count >= 500 {
				break
			}
			if _, ok := repo[p]; !ok || len(repo[p]) == 0 {
				log.Printf("Cannot find package %s in the indexes", p)
			}
			var packageComponent, packageSourceName string
			var artifacts []Artifact
			// TODO: Optionally only include the artifacts provided by a specific release.
			for a := range repo[p] {
				if matches := elementsRegex.FindStringSubmatch(a); matches != nil {
					component := matches[elementsRegex.SubexpIndex("component")]
					sourceName := matches[elementsRegex.SubexpIndex("sourceName")]
					version := matches[elementsRegex.SubexpIndex("version")]
					artifact := matches[elementsRegex.SubexpIndex("artifact")]
					if packageComponent == "" {
						packageComponent = component
					}
					if packageSourceName == "" {
						packageSourceName = sourceName
					}
					if !strings.HasPrefix(a, packageComponent+"/"+packageSourceName) {
						log.Printf("Package occurred with different prefixes: %s (%s vs %s)", p, packageComponent+"/"+packageSourceName, a)
						goto next
					}
					artifacts = append(artifacts, Artifact{Version: version, Name: artifact})
				} else {
					log.Fatalf("Unexpected artifact format %s", a)
				}
			}
			if packageComponent == "" || packageSourceName == "" {
				continue
			}
			slices.SortFunc(artifacts, func(a, b Artifact) int {
				return strings.Compare(fmt.Sprintf("%s/%s", a.Version, a.Name), fmt.Sprintf("%s/%s", b.Version, b.Name))
			})
			// TODO: Support multiple artifacts/versions for each package.
			ps.Packages = append(ps.Packages, benchmark.Package{Ecosystem: "debian", Name: packageComponent + "/" + packageSourceName, Versions: []string{artifacts[len(artifacts)-1].Version}, Artifacts: []string{artifacts[len(artifacts)-1].Name}})
			ps.Count += 1
		next:
		}
		ps.Updated = time.Now()
		return
	},
}

var debianTop500Stable = RebuildBenchmark{
	Filename: "debian_top_500_stable.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		var (
			// The "all" arch is included in other arch indices.
			archs         = []string{"amd64"}
			popularityURL = "https://popcon.debian.org/stable/by_inst"
			repositoryURL = "https://deb.debian.org/debian"
		)
		resp, err := get(ctx, popularityURL)
		if err != nil {
			log.Fatalf("error fetching popularity: %v", err)
		}
		b := bufio.NewScanner(resp)
		var popularPackages []string
		// Lines beginning with '#' are comments. Here are the column names and first row:
		// #rank name                            inst  vote   old recent no-files (maintainer)
		// 1     adduser                        248240 227236  4237 16731    36 (Debian Adduser Developers)
		for b.Scan() {
			line := strings.TrimSpace(b.Text())
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			// We only care about the package name.
			popularPackages = append(popularPackages, f[1])
		}
		// Mapping from package name to a set of strings like "component/sourceName/version/artifact" where package name matches the artifact.
		var elementsRegex = regexp.MustCompile(`^(?P<component>[^/]+)\/(?P<sourceName>[^/]+)\/(?P<version>[^/]+)\/(?P<artifact>[^/]+)$`)
		repo := map[string]map[string]bool{}
		for _, arch := range archs {
			index, err := get(ctx, repositoryURL+fmt.Sprintf("/indices/files/arch-%s.files", arch))
			if err != nil {
				log.Fatalf("error fetching arch %s index: %v", arch, err)
			}
			b = bufio.NewScanner(index)
			// Each line in the index is a relative path from the repository root. Files included are *.dsc, *.tar.gz, and *.deb.
			// And example would be:
			// ./pool/contrib/a/alex4/alex4_1.1-10+b2_amd64.deb
			packagePathRegex := regexp.MustCompile(`^\.\/pool\/(?P<component>[^/]+)\/[^/]+\/(?P<sourceName>[^/]+)\/(?P<packageName>[^_]+)_(?P<version>[^_]+)_[^_]+\.deb$`)
			for b.Scan() {
				if matches := packagePathRegex.FindStringSubmatch(strings.TrimSpace(b.Text())); matches != nil {
					component := matches[packagePathRegex.SubexpIndex("component")]
					sourceName := matches[packagePathRegex.SubexpIndex("sourceName")]
					packageName := matches[packagePathRegex.SubexpIndex("packageName")]
					version := matches[packagePathRegex.SubexpIndex("version")]
					artifact := filepath.Base(b.Text())
					if _, ok := repo[packageName]; !ok {
						repo[packageName] = map[string]bool{}
					}
					repo[packageName][fmt.Sprintf("%s/%s/%s/%s", component, sourceName, version, artifact)] = true
				}
			}
		}
		type Artifact struct {
			Version string
			Name    string
		}
		for _, p := range popularPackages {
			if ps.Count >= 500 {
				break
			}
			if _, ok := repo[p]; !ok || len(repo[p]) == 0 {
				log.Printf("Cannot find package %s in the indexes", p)
			}
			var packageComponent, packageSourceName string
			var artifacts []Artifact
			// TODO: Optionally only include the artifacts provided by a specific release.
			for a := range repo[p] {
				if matches := elementsRegex.FindStringSubmatch(a); matches != nil {
					component := matches[elementsRegex.SubexpIndex("component")]
					sourceName := matches[elementsRegex.SubexpIndex("sourceName")]
					version := matches[elementsRegex.SubexpIndex("version")]
					artifact := matches[elementsRegex.SubexpIndex("artifact")]
					if packageComponent == "" {
						packageComponent = component
					}
					if packageSourceName == "" {
						packageSourceName = sourceName
					}
					if !strings.HasPrefix(a, packageComponent+"/"+packageSourceName) {
						log.Printf("Package occurred with different prefixes: %s (%s vs %s)", p, packageComponent+"/"+packageSourceName, a)
						goto next
					}
					artifacts = append(artifacts, Artifact{Version: version, Name: artifact})
				} else {
					log.Fatalf("Unexpected artifact format %s", a)
				}
			}
			if packageComponent == "" || packageSourceName == "" {
				continue
			}
			slices.SortFunc(artifacts, func(a, b Artifact) int {
				return strings.Compare(fmt.Sprintf("%s/%s", a.Version, a.Name), fmt.Sprintf("%s/%s", b.Version, b.Name))
			})
			// TODO: Support multiple artifacts/versions for each package.
			ps.Packages = append(ps.Packages, benchmark.Package{Ecosystem: "debian", Name: packageComponent + "/" + packageSourceName, Versions: []string{artifacts[len(artifacts)-1].Version}, Artifacts: []string{artifacts[len(artifacts)-1].Name}})
			ps.Count += 1
		next:
		}
		ps.Updated = time.Now()
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
  COUNT(*) AS DirectRdeps,
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
  DirectRdeps DESC
LIMIT 2500
`)
		pkgs := make(chan struct {
			DirectRdeps int64
			Package     string
			Version     string
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
				DirectRdeps int64
				Package     string
				Version     string
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
  COUNT(*) AS DirectRdeps,
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
  DirectRdeps DESC
LIMIT 10000
`)
		pkgs := make(chan struct {
			DirectRdeps int64
			Package     string
			Version     string
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
				DirectRdeps int64
				Package     string
				Version     string
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
  COUNT(*) AS DirectRdeps,
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
  DirectRdeps DESC
LIMIT 2500
`)
		pkgs := make(chan struct {
			DirectRdeps int64
			Package     string
			Version     string
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
				DirectRdeps int64
				Package     string
				Version     string
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
			nameParts := strings.SplitN(p.Package, ":", 2)
			if len(nameParts) != 2 {
				fmt.Println("Agh unexpected: ", p.Package)
				return
			}
			// TODO: Find the artifact name from a real source, don't just guess.
			psp.Artifacts = append(psp.Artifacts, fmt.Sprintf("%s-%s.jar", nameParts[1], p.Version))
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var mavenRecentTop500 = RebuildBenchmark{
	Filename: "maven_recent_top_500.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
WITH
  LatestSnapshot AS (
    SELECT
      Time
    FROM
      ` + "`" + `bigquery-public-data.deps_dev_v1.Snapshots` + "`" + `
    ORDER BY
      Time DESC
    LIMIT
      1
  ),
  PackageVersionDownloads AS (
    SELECT
      COUNT(*) AS DirectRdeps,
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
      INNER JOIN LatestSnapshot
      ON
        LatestSnapshot.Time = T.SnapshotAt
      INNER JOIN (
        SELECT 
          Name,
          Version
        FROM
          ` + "`" + `bigquery-public-data.deps_dev_v1.PackageVersions` + "`" + ` as T
        INNER JOIN LatestSnapshot
        ON LatestSnapshot.Time = T.SnapshotAt
        WHERE T.UpstreamPublishedAt > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 2*365 DAY)
      ) AS Recent
      ON
        Recent.Name = T.` + "`" + `To` + "`" + `.Name AND Recent.Version = T.` + "`" + `To` + "`" + `.Version
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
    )
SELECT
  *
FROM PackageVersionDownloads
QUALIFY ROW_NUMBER() OVER(PARTITION BY SPLIT(Package, ':')[OFFSET(0)] ORDER BY DirectRdeps DESC) = 1
ORDER BY
  DirectRdeps DESC
LIMIT 2500
`)
		pkgs := make(chan struct {
			DirectRdeps int64
			Package     string
			Version     string
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
				DirectRdeps int64
				Package     string
				Version     string
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
		groups := make(map[string]bool)
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
			if len(psp.Versions) >= 1 {
				continue
			}
			nameParts := strings.SplitN(p.Package, ":", 2)
			if len(nameParts) != 2 {
				fmt.Println("Agh unexpected: ", p.Package)
				return
			}
			if groups[nameParts[0]] {
				continue
			}
			groups[nameParts[0]] = true
			// TODO: Find the artifact name from a real source, don't just guess.
			psp.Artifacts = append(psp.Artifacts, fmt.Sprintf("%s-%s.jar", nameParts[1], p.Version))
			psp.Versions = append(psp.Versions, p.Version)
		}
		for _, psp := range ps.Packages {
			ps.Count += len(psp.Versions)
		}
		ps.Updated = now
		return
	},
}

var mavenRecentAll = RebuildBenchmark{
	Filename: "maven_recent_all.json",
	Generator: func(ctx context.Context) (ps benchmark.PackageSet) {
		now := time.Now()
		client, err := bigquery.NewClient(ctx, *project, option.WithQuotaProject(*project))
		if err != nil {
			log.Fatal(err.Error())
		}
		query := client.Query(`
WITH
	LatestSnapshot AS (
    SELECT
      Time
    FROM
      ` + "`" + `bigquery-public-data.deps_dev_v1.Snapshots` + "`" + `
    ORDER BY
      Time DESC
    LIMIT
      1
  )
SELECT
  T.Name as Package,
  T.Version as Version
FROM
	` + "`" + `bigquery-public-data.deps_dev_v1.PackageVersions` + "`" + ` T
INNER JOIN
  LatestSnapshot
  ON LatestSnapshot.Time = T.SnapshotAt
WHERE
  T.System = "MAVEN" AND T.UpstreamPublishedAt IS NOT NULL AND EXTRACT(YEAR FROM UpstreamPublishedAt) >= 2020
QUALIFY
  ROW_NUMBER() OVER (PARTITION BY SPLIT(Package, ':')[OFFSET(0)] ORDER BY T.UpstreamPublishedAt DESC) = 1
`)
		pkgs := make(chan struct {
			Package string
			Version string
		}, 100)
		// Get package versions from deps.dev's dependency table.
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
				Package string
				Version string
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
			nameParts := strings.SplitN(p.Package, ":", 2)
			if len(nameParts) != 2 {
				fmt.Println("Agh unexpected: ", p.Package)
				return
			}
			// TODO: Find the artifact name from a real source, don't just guess.
			pkg := benchmark.Package{
				Name:      p.Package,
				Ecosystem: "maven",
				Versions:  []string{p.Version},
				Artifacts: []string{fmt.Sprintf("%s-%s.jar", nameParts[1], p.Version)},
			}
			ps.Packages = append(ps.Packages, pkg)
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
