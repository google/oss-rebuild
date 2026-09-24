// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"slices"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestMatchTag(t *testing.T) {
	tests := []struct {
		tag     string
		pkg     string
		version string
		strict  bool
		approx  bool
	}{
		{"v1.0.0", "mypackage", "1.0.0", true, true},
		{"v1.0.0+meta", "mypackage", "1.0.0+meta", true, true},
		{"v1x0x0", "mypackage", "1.0.0", false, false},
		{"v1.0.0-rc1", "mypackage", "1.0.0", false, true},
		{"mypackage-1.0.0", "mypackage", "1.0.0", true, true},
		{"1.0.0", "mypackage", "1.0.0", true, true},
		{"v1.0.1", "mypackage", "1.0.0", false, false},
		{"v1.0", "mypackage", "1.0.0", false, false},
		{"v1", "mypackage", "1.0.0", false, false},
		{"org/mypackage-1.0.0", "org/mypackage", "1.0.0", true, true},
		{"mypackage-1.0.0", "org/mypackage", "1.0.0", true, true},
		{"org/otherpackage-1.0.0", "org/mypackage", "1.0.0", false, true}, // org-but-not-package special case
		{"otherpackage-1.0.0", "org/mypackage", "1.0.0", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			strict, approx := MatchTag(tt.tag, tt.pkg, tt.version)
			if strict != tt.strict || approx != tt.approx {
				t.Errorf("MatchTag(%q, %q, %q) = (%v, %v), want (%v, %v)", tt.tag, tt.pkg, tt.version, strict, approx, tt.strict, tt.approx)
			}
		})
	}
}

func TestSortTagMatches(t *testing.T) {
	tests := []struct {
		name    string
		matches []string
		pkg     string
		version string
		want    []string
	}{
		{
			name:    "package tag beats sibling and repo tag",
			matches: []string{"0.2.0", "grep-0.2.0", "grep-printer-0.2.0"},
			pkg:     "grep",
			version: "0.2.0",
			want:    []string{"grep-0.2.0", "0.2.0", "grep-printer-0.2.0"},
		},
		{
			name:    "repo tag beats an unrelated sibling",
			matches: []string{"aaa-v1.0.0", "v1.0.0"},
			pkg:     "mypackage",
			version: "1.0.0",
			want:    []string{"v1.0.0", "aaa-v1.0.0"},
		},
		{
			name:    "scoped npm name matches an unscoped tag",
			matches: []string{"babel-types@7.0.0", "core@7.0.0", "v7.0.0"},
			pkg:     "@babel/core",
			version: "7.0.0",
			want:    []string{"core@7.0.0", "v7.0.0", "babel-types@7.0.0"},
		},
		{
			name:    "maven coordinate matches the artifact tag",
			matches: []string{"guava-33.0.0", "other-33.0.0"},
			pkg:     "com.google.guava:guava",
			version: "33.0.0",
			want:    []string{"guava-33.0.0", "other-33.0.0"},
		},
		{
			name:    "decorated package name beats a sibling",
			matches: []string{"python-mypkg-langchain-v1.0.0", "python-mypkg-openai-v1.0.0"},
			pkg:     "mypkg-openai",
			version: "1.0.0",
			want:    []string{"python-mypkg-openai-v1.0.0", "python-mypkg-langchain-v1.0.0"},
		},
		{
			name:    "unranked tags keep lexicographic order",
			matches: []string{"zzz-1.0.0", "aaa-1.0.0"},
			pkg:     "mypackage",
			version: "1.0.0",
			want:    []string{"aaa-1.0.0", "zzz-1.0.0"},
		},
		{
			name:    "separator and v-prefix variants all read as the package name",
			matches: []string{"unrelated-1.0.0", "mypackage/v1.0.0"},
			pkg:     "mypackage",
			version: "1.0.0",
			want:    []string{"mypackage/v1.0.0", "unrelated-1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slices.Clone(tt.matches)
			sortTagMatches(got, tt.pkg, tt.version)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("sortTagMatches(%v, %q, %q) diff (-want +got):\n%s", tt.matches, tt.pkg, tt.version, diff)
			}
		})
	}
}

func TestFindTagMatch(t *testing.T) {
	repo := must(git.Init(memory.NewStorage(), memfs.New()))

	c1 := createCommit(repo, "commit1")
	c2 := createCommit(repo, "commit2")
	c3 := createCommit(repo, "commit3")
	createLightweightTag(repo, "v1.0.0", c1)
	createLightweightTag(repo, "v1.1.0", c2)
	createAnnotatedTag(repo, "v1.0.0-alpha", c3)

	tests := []struct {
		pkg     string
		version string
		want    string
		wantErr bool
	}{
		{"mypackage", "1.0.0", c1, false},
		{"mypackage", "1.1.0", c2, false},
		{"otherpackage", "1.0.0", c1, false},
		{"otherpackage", "1.0.0-alpha", c3, false},
		{"mypackage", "2.0.0", "", false}, // No match
		// TODO: Add error test cases.
	}

	for _, tt := range tests {
		t.Run(tt.pkg+"-"+tt.version, func(t *testing.T) {
			got, err := FindTagMatch(tt.pkg, tt.version, repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindTagMatch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("FindTagMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindTagMatchOnlyResolvesFirstMatch(t *testing.T) {
	storer := &failingTagStorage{Storer: memory.NewStorage()}
	repo := must(git.Init(storer, memfs.New()))
	commit := createCommit(repo, "commit")
	createLightweightTag(repo, "mypackage-1.0.0", commit)
	createLightweightTag(repo, "zzz-1.0.0", commit)
	storer.fail = plumbing.NewTagReferenceName("zzz-1.0.0")

	got, err := FindTagMatch("mypackage", "1.0.0", repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Errorf("FindTagMatch() = %q, want %q", got, commit)
	}
	if _, err := FindTagMatches("mypackage", "1.0.0", repo); err == nil {
		t.Error("FindTagMatches() error = nil")
	}
}

type failingTagStorage struct {
	storage.Storer
	fail plumbing.ReferenceName
}

func (s *failingTagStorage) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if name == s.fail {
		return nil, plumbing.ErrReferenceNotFound
	}
	return s.Storer.Reference(name)
}

func TestAllTags(t *testing.T) {
	repo := must(git.Init(memory.NewStorage(), memfs.New()))

	c1 := createCommit(repo, "commit1")
	c2 := createCommit(repo, "commit2")
	c3 := createCommit(repo, "commit3")
	createLightweightTag(repo, "v1.0.0", c1)
	createLightweightTag(repo, "v1.1.0", c2)
	createAnnotatedTag(repo, "v1.0.0-alpha", c3)

	want := []string{"v1.0.0", "v1.1.0", "v1.0.0-alpha"}

	got, err := allTags(repo)
	if err != nil {
		t.Errorf("allTags() error = %v", err)
	}

	stringLess := func(a, b string) bool { return a < b }
	if diff := cmp.Diff(got, want, cmpopts.SortSlices(stringLess)); diff != "" {
		t.Errorf("allTags() diff\n%s", diff)
	}
}

func createCommit(repo *git.Repository, name string) string {
	worktree := must(repo.Worktree())
	must(worktree.Filesystem.Create(name))
	must(worktree.Add(name))
	commit := must(worktree.Commit("Test commit", &git.CommitOptions{
		Author:    &object.Signature{Name: "Test Author", Email: "test@example.com"},
		Committer: &object.Signature{Name: "Test Author", Email: "test@example.com"},
	}))
	return commit.String()
}

func createLightweightTag(repo *git.Repository, tag, targetCommit string) {
	commit := must(repo.CommitObject(plumbing.NewHash(targetCommit)))
	must(repo.CreateTag(tag, commit.Hash, nil))
}

func createAnnotatedTag(repo *git.Repository, tag, targetCommit string) {
	commit := must(repo.CommitObject(plumbing.NewHash(targetCommit)))
	must(repo.CreateTag(tag, commit.Hash, &git.CreateTagOptions{
		Message: "Test annotated tag",
		Tagger:  &object.Signature{Name: "Test Author", Email: "test@example.com"},
	}))
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
