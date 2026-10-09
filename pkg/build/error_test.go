// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import "testing"

func TestExitErrorWording(t *testing.T) {
	for _, tc := range []struct {
		e    ExitError
		want string
	}{
		{ExitError{Phase: "build", Code: 2, Command: "npm run build"}, "build failed in build phase with exit code 2; the failing command was `npm run build`"},
		{ExitError{Phase: "container run", Command: "cargo package"}, "build failed in container run phase; the failing command was `cargo package`"},
		{ExitError{Phase: "image build", Code: 1}, "build failed in image build phase with exit code 1"},
	} {
		if got := tc.e.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

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
