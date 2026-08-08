// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestCratesIOCargoPackageQuotesDirectory(t *testing.T) {
	for _, dir := range []string{"package with spaces", "package's source"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			want := filepath.Join(root, dir)
			if err := os.MkdirAll(want, 0o755); err != nil {
				t.Fatal(err)
			}
			strategy := &CratesIOCargoPackage{
				Location:    rebuild.Location{Dir: dir},
				RustVersion: "1.77.0",
			}
			if got := strategy.ToWorkflow().Build[0].With["dir"]; got != dir {
				t.Fatalf("dir = %q, want raw value %q", got, dir)
			}
			instructions, err := strategy.GenerateFor(rebuild.Target{Artifact: "test.crate"}, rebuild.BuildEnv{HasRepo: true})
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(instructions.Build, "/root/.cargo/bin/cargo package --no-verify", "pwd", 1)
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("build script failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != want {
				t.Fatalf("pwd = %q, want %q", got, want)
			}
		})
	}
}
