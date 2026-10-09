// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package buildpolicy

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	script := "# pin the toolchain\n  npm   install -g pnpm@9  # as the publisher had\n\nnpm run build\necho '# not a comment' > /tmp/x\n"
	want := []string{"npm install -g pnpm@9", "npm run build", "echo '# not a comment' > /tmp/x"}
	if got := Commands(script); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Commands = %q, want %q", got, want)
	}
}
