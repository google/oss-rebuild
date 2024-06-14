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
		{"github.com/user/repo", "https://github.com/user/repo", false},                        // GitHub, basic
		{"github:user/repo", "https://github.com/user/repo", false},                            // GitHub, alt format
		{"https://github.com/org/project.git", "https://github.com/org/project", false},        // GitHub, with .git
		{"http://github.com/org/project/tree/branch", "https://github.com/org/project", false}, // GitHub, with path
		{"GitLab.com/Group/Repo", "https://gitlab.com/group/repo", false},                      // GitLab, case insensitive
		{"https://bitbucket.org/team/repo", "https://bitbucket.org/team/repo", false},          // Bitbucket
		{"github.com/user/..", "", true},                                                       // Invalid repo name
		{"github.com/user/.", "", true},                                                        // Invalid repo name
		{"https://foo.com", "https://foo.com", false},                                          // Unknown URL
		{"https://foo.com/path.git", "https://foo.com/path.git", false},                        // Unknown URL, retain .git
		{"https://foo.com/this/path?this=query", "https://foo.com/this/path", false},           // Unknown URL, strip query
		{"https://Foo.com/this/path", "https://foo.com/this/path", false},                      // Unknown URL, case insensitive domain
		{"https://Foo.com/This/Path", "https://foo.com/This/Path", false},                      // Unknown URL, case sensitive
		{"ssh://git@foo.com/path", "", true},                                                   // SSH URL
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
