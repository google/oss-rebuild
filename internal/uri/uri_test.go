// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package uri

import (
	"testing"
)

func TestFindARepo(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},       // Empty input
		{"foobar", ""}, // Invalid URL
		// FindARepo doesn't canonicallize.
		{"github.com/user/repo", "github.com/user/repo"},                        // GitHub, basic
		{"github:user/repo", "github:user/repo"},                                // GitHub, alt format
		{"https://github.com/org/project.git", "github.com/org/project.git"},    // GitHub, with .git
		{"http://github.com/org/project/tree/branch", "github.com/org/project"}, // GitHub, with path
		{"GitLab.com/Group/Repo", "GitLab.com/Group/Repo"},                      // GitLab, case insensitive
		{"https://bitbucket.org/team/repo", "bitbucket.org/team/repo"},          // Bitbucket
		{
			"https://gitlab.com/245project/bevy-plugin/bevy-2dviewangle",
			"gitlab.com/245project/bevy-plugin/bevy-2dviewangle",
		},
		{
			"https://gitlab.com/group/subgroup/repo/-/tree/main",
			"gitlab.com/group/subgroup/repo",
		},
		{
			"https://gitlab.com/group/repo/tree/main",
			"gitlab.com/group/repo",
		},
		{
			"https://gitlab.com/group/tree/repo",
			"gitlab.com/group/tree/repo",
		},
		{
			"https://img.shields.io/github/stars/rust-lang/rust",
			"",
		},
		{
			"https://img.shields.io/gitlab/pipeline/group/repo/main",
			"",
		},
		{
			"[![b](https://img.shields.io/github/v/tag/o/r)](https://github.com/org/project)",
			"github.com/org/project",
		},
		{
			"https://gitlab.com/group/subgroup/repo/blob/main/README.md",
			"gitlab.com/group/subgroup/repo",
		},
		{
			"https://gitlab.com/group/repo/badges/main/pipeline.svg",
			"gitlab.com/group/repo",
		},
		{
			"https://gitlab.com/group/repo/issues",
			"gitlab.com/group/repo",
		},
		{
			"https://gitlab.com/group/blob/repo",
			"gitlab.com/group/blob/repo",
		},
	}
	for _, test := range tests {
		actual := FindCommonRepo(test.input)
		if actual != test.expected {
			t.Errorf("FindARepo(%s) = %s, expected %s", test.input, actual, test.expected)
		}
	}
}

func TestCanonicalizeRepoURI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{"", "", true},    // Empty input
		{"foo", "", true}, // Invalid URL
		{"github.com/user/repo", "https://github.com/user/repo", false},                                                 // GitHub, basic
		{"github:user/repo", "https://github.com/user/repo", false},                                                     // GitHub, alt format
		{"https://github.com/org/project.git", "https://github.com/org/project", false},                                 // GitHub, with .git
		{"http://github.com/org/project/tree/branch", "https://github.com/org/project", false},                          // GitHub, with path
		{"GitLab.com/Group/Repo", "https://gitlab.com/group/repo", false},                                               // GitLab, case insensitive
		{"https://bitbucket.org/team/repo", "https://bitbucket.org/team/repo", false},                                   // Bitbucket
		{"github.com/user/..", "", true},                                                                                // Invalid repo name
		{"github.com/user/.", "", true},                                                                                 // Invalid repo name
		{"https://foo.com", "https://foo.com", false},                                                                   // Unknown URL
		{"https://foo.com/path.git", "https://foo.com/path.git", false},                                                 // Unknown URL, retain .git
		{"https://foo.com/this/path?this=query", "https://foo.com/this/path", false},                                    // Unknown URL, strip query
		{"https://Foo.com/this/path", "https://foo.com/this/path", false},                                               // Unknown URL, case insensitive domain
		{"https://Foo.com/This/Path", "https://foo.com/This/Path", false},                                               // Unknown URL, case sensitive
		{"ssh://git@foo.com/path", "", true},                                                                            // SSH URL
		{"https://us-east1-proj.sourcemanager.dev/org/repo", "https://us-east1-proj.sourcemanager.dev/org/repo", false}, // SSM URL
		{
			"https://gitlab.com/245project/bevy-plugin/bevy-2dviewangle",
			"https://gitlab.com/245project/bevy-plugin/bevy-2dviewangle",
			false,
		},
		{
			"git@gitlab.com:group/subgroup/repo.git",
			"https://gitlab.com/group/subgroup/repo",
			false,
		},
		{
			"https://gitlab.com/group/subgroup/repo/-/tree/main",
			"https://gitlab.com/group/subgroup/repo",
			false,
		},
		{
			"https://gitlab.com/group/repo/tree/main",
			"https://gitlab.com/group/repo",
			false,
		},
		{
			"https://gitlab.com/group/tree/repo/-/tree/main",
			"https://gitlab.com/group/tree/repo",
			false,
		},
		{
			"https://gitlab.com/group/subgroup/repo/blob/main/README.md",
			"https://gitlab.com/group/subgroup/repo",
			false,
		},
		{
			"https://gitlab.com/group/subgroup/repo/-/blob/main/README.md",
			"https://gitlab.com/group/subgroup/repo",
			false,
		},
		{
			"https://gitlab.com/group/repo/badges/main/pipeline.svg",
			"https://gitlab.com/group/repo",
			false,
		},
	}

	for _, test := range tests {
		actual, err := CanonicalizeRepoURI(test.input)
		if (err != nil) != test.wantErr {
			t.Errorf("CanonicalizeRepoURI(%s) error = %v, wantErr %v", test.input, err, test.wantErr)
		}
		if actual != test.expected {
			t.Errorf("CanonicalizeRepoURI(%s) = %s, expected %s", test.input, actual, test.expected)
		}
	}
}
