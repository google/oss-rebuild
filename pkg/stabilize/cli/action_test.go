// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/oss-rebuild/pkg/act/cli"
)

func TestStabilizeFile(t *testing.T) {
	// Create a temp input file (tar)
	dir := t.TempDir()
	infile := filepath.Join(dir, "input.tar")
	outfile := filepath.Join(dir, "output.tar")

	f, err := os.Create(infile)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	// Add an entry
	tw.WriteHeader(&tar.Header{Name: "test.txt", Size: 4, Mode: 0600})
	tw.Write([]byte("test"))
	tw.Close()
	f.Close()

	cfg := Config{
		Infile:        infile,
		Outfile:       outfile,
		EnablePasses:  []string{"all"},
		DisablePasses: []string{"none"},
	}

	deps := &Deps{}
	deps.SetIO(cli.IO{Out: os.Stdout, Err: os.Stderr})

	_, err = StabilizeFile(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("StabilizeFile failed: %v", err)
	}

	if _, err := os.Stat(outfile); os.IsNotExist(err) {
		t.Errorf("Output file not created")
	}
}
