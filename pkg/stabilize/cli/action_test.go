// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
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

// TestStabilizeFileNaming covers how the ecosystem and artifact name are
// resolved: from the input's file name by default, from -ecosystem when
// given, and from -artifact when the input path does not carry the name.
func TestStabilizeFileNaming(t *testing.T) {
	tgz := must(archivetest.TgzFile([]archive.TarEntry{{Header: &tar.Header{Name: "package/index.js", Size: 2, Mode: 0644}, Body: []byte("1\n")}})).Bytes()
	for _, tc := range []struct {
		name    string
		infile  string
		cfg     Config
		wantErr string
	}{
		{name: "FromFileName", infile: "/pkg-1.0.0.tgz"},
		{name: "ExplicitEcosystem", infile: "/pkg-1.0.0.tgz", cfg: Config{Ecosystem: "npm"}},
		{name: "ArtifactNamesBareInput", infile: "/builds/iter-1/out/rebuild", cfg: Config{Ecosystem: "npm", Artifact: "pkg-1.0.0.tgz"}},
		{name: "BareInputUnnamed", infile: "/builds/iter-1/out/rebuild", wantErr: "no eligible ecosystems"},
		{name: "UnknownEcosystem", infile: "/pkg-1.0.0.tgz", cfg: Config{Ecosystem: "bogus"}, wantErr: "unknown archive format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := memfs.New()
			f := must(fsys.Create(tc.infile))
			f.Write(tgz)
			f.Close()
			cfg := tc.cfg
			cfg.Infile, cfg.Outfile = tc.infile, "/out"
			cfg.EnablePasses, cfg.DisablePasses = []string{"all"}, []string{"none"}
			deps := &Deps{FS: fsys}
			deps.SetIO(cli.IO{Out: io.Discard, Err: io.Discard})
			_, err := StabilizeFile(context.Background(), cfg, deps)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("StabilizeFile: %v", err)
			}
			if _, err := fsys.Stat("/out"); err != nil {
				t.Errorf("output: %v", err)
			}
		})
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
