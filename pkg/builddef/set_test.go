// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package builddef

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"gopkg.in/yaml.v3"
)

func TestFilesystemBuildDefinitionSet_Path(t *testing.T) {
	mfs := memfs.New()
	bds := NewFilesystemBuildDefinitionSet(mfs)
	target := rebuild.Target{
		Ecosystem: rebuild.NPM,
		Package:   "test-package",
		Version:   "1.0.0",
		Artifact:  "test-package-1.0.0.tgz",
	}
	ctx := context.Background()
	path, err := bds.Path(ctx, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedPath := "/npm/test-package/1.0.0/test-package-1.0.0.tgz/build.yaml"
	if path != expectedPath {
		t.Errorf("path mismatch: expected %s, got %s", expectedPath, path)
	}
}

func TestFilesystemBuildDefinitionSet_Get(t *testing.T) {
	tests := []struct {
		name         string
		setupFS      func(fs billy.Filesystem, target rebuild.Target)
		target       rebuild.Target
		wantStrategy schema.StrategyOneOf
		wantErr      string
	}{
		{
			name:    "file not found",
			setupFS: func(fs billy.Filesystem, target rebuild.Target) { return },
			target: rebuild.Target{
				Ecosystem: rebuild.NPM,
				Package:   "test-package",
				Version:   "1.0.0",
				Artifact:  "test-package-1.0.0.tgz",
			},
			wantErr: fs.ErrNotExist.Error(),
		},
		{
			name: "valid strategy",
			setupFS: func(fs billy.Filesystem, target rebuild.Target) {
				asset := rebuild.BuildDef.For(target)
				assetPath := filepath.Dir(asset.Target.Artifact)
				orDie(fs.MkdirAll(assetPath, 0755))
				strategyOneOf := schema.NewStrategyOneOf(&rebuild.LocationHint{
					Location: rebuild.Location{
						Repo: "https://github.com/test/repo",
						Ref:  "main",
						Dir:  ".",
					},
				})
				f := must(fs.Create("/npm/test-package/1.0.0/test-package-1.0.0.tgz/build.yaml"))
				defer f.Close()
				orDie(yaml.NewEncoder(f).Encode(schema.BuildDefinition{StrategyOneOf: &strategyOneOf}))
			},
			target: rebuild.Target{
				Ecosystem: rebuild.NPM,
				Package:   "test-package",
				Version:   "1.0.0",
				Artifact:  "test-package-1.0.0.tgz",
			},
			wantStrategy: schema.NewStrategyOneOf(&rebuild.LocationHint{
				Location: rebuild.Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
					Dir:  ".",
				},
			}),
		},
		{
			name: "invalid yaml",
			setupFS: func(fs billy.Filesystem, target rebuild.Target) {
				asset := rebuild.BuildDef.For(target)
				assetPath := filepath.Dir(asset.Target.Artifact)
				orDie(fs.MkdirAll(assetPath, 0755))
				f := must(fs.Create("/npm/test-package/1.0.0/test-package-1.0.0.tgz/build.yaml"))
				defer f.Close()
				must(f.Write([]byte("invalid: yaml: : content")))
			},
			target: rebuild.Target{
				Ecosystem: rebuild.NPM,
				Package:   "test-package",
				Version:   "1.0.0",
				Artifact:  "test-package-1.0.0.tgz",
			},
			wantErr: "parsing build definition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mfs := memfs.New()
			bds := NewFilesystemBuildDefinitionSet(mfs)
			tt.setupFS(mfs, tt.target)
			ctx := context.Background()
			got, err := bds.Get(ctx, tt.target)
			// Check error expectations
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantS := must(tt.wantStrategy.Strategy())
			gotS, err := got.Strategy()
			if err != nil {
				t.Fatalf("failed to get actual strategy: %v", err)
			}
			if diff := cmp.Diff(wantS, gotS); diff != "" {
				t.Errorf("strategy mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}
