// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"archive/tar"
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/act/cli"
)

func TestStabilizeFile(t *testing.T) {
	// Create a temp input file (tar)
	fsys := memfs.New()
	infile := "/input.tar"
	outfile := "/output.tar"

	f, err := fsys.Create(infile)
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

	deps := &Deps{
		FS: fsys,
	}
	deps.SetIO(cli.IO{Out: os.Stdout, Err: os.Stderr})

	_, err = StabilizeFile(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("StabilizeFile failed: %v", err)
	}

	if _, err := fsys.Stat(outfile); errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Output file not created")
	}
}
