// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/msuozzo/go-git2"
)

// mavenReleases are the Apache Maven 3 releases with the dates they reached
// Maven Central.
var mavenReleases = []struct{ Version, Date string }{
	{"3.0.1", "2010-11-23"}, {"3.0.2", "2011-01-09"}, {"3.0.3", "2011-02-28"}, {"3.0.4", "2012-01-17"},
	{"3.0.5", "2013-02-19"}, {"3.1.0", "2013-06-28"}, {"3.1.1", "2013-09-17"}, {"3.2.1", "2014-02-14"},
	{"3.2.2", "2014-06-17"}, {"3.2.3", "2014-08-11"}, {"3.2.5", "2014-12-14"}, {"3.3.1", "2015-03-13"},
	{"3.3.3", "2015-04-22"}, {"3.3.9", "2015-11-10"}, {"3.5.0", "2017-04-03"}, {"3.5.2", "2017-10-18"},
	{"3.5.3", "2018-02-24"}, {"3.5.4", "2018-06-17"}, {"3.6.0", "2018-10-24"}, {"3.6.1", "2019-04-04"},
	{"3.6.2", "2019-08-27"}, {"3.6.3", "2019-11-19"}, {"3.8.1", "2021-03-30"}, {"3.8.2", "2021-08-04"},
	{"3.8.3", "2021-09-27"}, {"3.8.4", "2021-11-14"}, {"3.8.5", "2022-03-05"}, {"3.8.6", "2022-06-06"},
	{"3.8.7", "2022-12-24"}, {"3.9.0", "2023-01-31"}, {"3.8.8", "2023-03-08"}, {"3.9.1", "2023-03-15"},
	{"3.9.2", "2023-05-08"}, {"3.9.3", "2023-06-23"}, {"3.9.4", "2023-07-31"}, {"3.9.5", "2023-10-02"},
	{"3.9.6", "2023-11-28"}, {"3.9.7", "2024-05-22"}, {"3.9.8", "2024-06-13"}, {"3.9.9", "2024-08-14"},
	{"3.9.10", "2025-06-01"}, {"3.8.9", "2025-06-14"}, {"3.9.11", "2025-07-12"}, {"3.9.12", "2025-12-13"},
	{"3.9.13", "2026-03-03"}, {"3.9.14", "2026-03-09"}, {"3.9.15", "2026-04-13"}, {"3.9.16", "2026-05-13"},
}

// mavenAt is the newest Maven release out by t: the one a publisher building
// then most likely had. Maintenance releases of an older line do not count
// as newer.
func mavenAt(t time.Time) string {
	best := mavenReleases[0].Version
	day := t.UTC().Format(time.DateOnly)
	for _, r := range mavenReleases {
		if r.Date <= day && compareVersions(r.Version, best) > 0 {
			best = r.Version
		}
	}
	return best
}

// compareVersions orders dotted numeric versions.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

var wrapperDistributionRe = regexp.MustCompile(`apache-maven/([^/]+)/apache-maven-`)

// wrapperMaven is the Maven version a Maven wrapper pins in dir or the
// repository root, or "" if neither has one.
func wrapperMaven(commit *git2.Commit, dir string) string {
	tree, err := commit.Tree()
	if err != nil {
		return ""
	}
	for _, d := range []string{dir, ""} {
		f, err := tree.File(path.Join(d, ".mvn/wrapper/maven-wrapper.properties"))
		if err != nil {
			continue
		}
		props, err := f.Contents()
		if err != nil {
			continue
		}
		for line := range strings.Lines(props) {
			k, v, _ := strings.Cut(line, "=")
			if strings.TrimSpace(k) != "distributionUrl" {
				continue
			}
			if m := wrapperDistributionRe.FindStringSubmatch(v); m != nil {
				return m[1]
			}
		}
	}
	return ""
}
