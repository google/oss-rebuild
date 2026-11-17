// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/msuozzo/go-git2"
)

func TestWrapperMaven(t *testing.T) {
	const (
		rootProps = ".mvn/wrapper/maven-wrapper.properties"
		dirProps  = "sub/" + rootProps
		v381      = "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.8.1/apache-maven-3.8.1-bin.zip\n"
		v396      = "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.6/apache-maven-3.9.6-bin.zip\n"
	)
	for _, tc := range []struct {
		name  string
		files gitxtest.FileContent
		want  string
	}{
		{"DirOverRoot", gitxtest.FileContent{dirProps: v381, rootProps: v396}, "3.8.1"},
		{"RootWhenDirHasNone", gitxtest.FileContent{rootProps: v396}, "3.9.6"},
		{"RootWhenDirNamesNoRelease", gitxtest.FileContent{dirProps: "wrapperVersion=3.3.2\n", rootProps: v396}, "3.9.6"},
		{"LaterLine", gitxtest.FileContent{dirProps: "# comment\nwrapperVersion=3.3.2\n" + v381}, "3.8.1"},
		{"NoWrapper", gitxtest.FileContent{"pom.xml": "<project/>\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "c", Files: tc.files}}, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["c"])))
			if diff := cmp.Diff(tc.want, wrapperMaven(commit, "sub")); diff != "" {
				t.Errorf("wrapperMaven() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMavenAt(t *testing.T) {
	for _, tc := range []struct{ day, want string }{
		{"2009-01-01", "3.0.1"},
		{"2018-03-01", "3.5.3"},
		{"2023-03-10", "3.9.0"}, // 3.8.8 came out after 3.9.0
		{"2025-06-20", "3.9.10"},
	} {
		day, _ := time.Parse(time.DateOnly, tc.day)
		if got := mavenAt(day); got != tc.want {
			t.Errorf("mavenAt(%s) = %s, want %s", tc.day, got, tc.want)
		}
	}
}
