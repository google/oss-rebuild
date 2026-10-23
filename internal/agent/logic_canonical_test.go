// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import "testing"

func TestSplitCanonical(t *testing.T) {
	for _, tc := range []struct {
		name, out, deps, build string
		ok                     bool
	}{
		{"Plain", "npm ci\n===BUILD===\nnpm pack", "npm ci", "npm pack", true},
		{"Fenced", "```bash\nnpm ci\n```\n===BUILD===\n```sh\nnpm pack\n```", "npm ci", "npm pack", true},
		{"ShellFence", "```shell\nnpm ci\n```\n===BUILD===\n```\nnpm pack\n```", "npm ci", "npm pack", true},
		{"NoSeparator", "npm ci\nnpm pack", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, build, ok := splitCanonical(tc.out)
			if deps != tc.deps || build != tc.build || ok != tc.ok {
				t.Errorf("splitCanonical = %q, %q, %v; want %q, %q, %v", deps, build, ok, tc.deps, tc.build, tc.ok)
			}
		})
	}
}
