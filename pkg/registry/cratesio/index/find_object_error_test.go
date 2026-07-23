// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"errors"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
)

func TestFindRegistryResolutionPropagatesObjectErrors(t *testing.T) {
	for _, objectName := range []string{"root tree", "directory tree", "blob"} {
		t.Run(objectName, func(t *testing.T) {
			storage := memory.NewStorage()
			repo, err := gitxtest.CreateRepoFromYAML(`commits:
  - id: publish
    files:
      de/pe/dependency: |
        {"name":"dependency","vers":"1.0.0"}
`, &gitxtest.RepositoryOptions{Storer: storage})
			if err != nil {
				t.Fatal(err)
			}
			commit, err := repo.CommitObject(repo.Commits["publish"])
			if err != nil {
				t.Fatal(err)
			}
			tree, err := commit.Tree()
			if err != nil {
				t.Fatal(err)
			}

			var hash plumbing.Hash
			switch objectName {
			case "root tree":
				hash = commit.TreeHash
			case "directory tree":
				entry, err := tree.FindEntry("de")
				if err != nil {
					t.Fatal(err)
				}
				hash = entry.Hash
			case "blob":
				entry, err := tree.FindEntry(EntryPath("dependency"))
				if err != nil {
					t.Fatal(err)
				}
				hash = entry.Hash
			}
			delete(storage.Objects, hash)

			_, err = FindRegistryResolution(
				[]*git.Repository{repo.Repository},
				[]cargolock.Package{{Name: "dependency", Version: "1.0.0"}},
				time.Now(),
				nil,
			)
			if !errors.Is(err, plumbing.ErrObjectNotFound) {
				t.Fatalf("FindRegistryResolution() error = %v, want %v", err, plumbing.ErrObjectNotFound)
			}
			if errors.Is(err, errNoMatches) {
				t.Fatalf("FindRegistryResolution() classified object failure as an absent package: %v", err)
			}
		})
	}
}

func TestFindRegistryResolutionTreatsMissingPathAsAbsent(t *testing.T) {
	repo := mustCreateRepo(t, `commits:
  - id: publish
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0"}
`)
	_, err := FindRegistryResolution(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{{Name: "dependency", Version: "1.0.0"}},
		time.Now(),
		nil,
	)
	if !errors.Is(err, errNoMatches) {
		t.Fatalf("FindRegistryResolution() error = %v, want %v", err, errNoMatches)
	}
}
