// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import "testing"

func TestLastTracedCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{name: "Empty", out: "", want: ""},
		{name: "NoTrace", out: "building...\nerror: boom\n", want: ""},
		{name: "LastTraceWins", out: "+ cd /src\n+ npm install\nnpm ERR! boom\n", want: "npm install"},
		{name: "NestedSubshell", out: "+ cd /src\n++ git rev-parse HEAD\n", want: "git rev-parse HEAD"},
		{name: "BareMarkerIgnored", out: "+ make\n+\n+ \n", want: "make"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastTracedCommand([]byte(tc.out)); got != tc.want {
				t.Errorf("LastTracedCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}
