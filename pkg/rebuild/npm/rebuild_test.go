// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestCompare(t *testing.T) {
	testCases := []struct {
		test     string
		target   rebuild.Target
		rebuild  []*archive.TarEntry
		upstream []*archive.TarEntry
		inst     rebuild.Instructions
		expected error
	}{
		{
			test:   "success",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: nil,
		},
		{
			test:   "dist",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/dist/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictMissingDist,
		},
		{
			test:   "ds_store",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/.DS_STORE", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictDSStore,
		},
		{
			test:   "crlf",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 6, Mode: 0644}, Body: []byte("stuff\n")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 7, Mode: 0644}, Body: []byte("stuff\r\n")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictLineEndings,
		},
		{
			test:   "mismatched_files",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file1", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#b")},
				{Header: &tar.Header{Name: "package/file2", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictMismatchedFiles,
		},
		{
			test:   "upstream_files",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
				{Header: &tar.Header{Name: "package/fileextra", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictUpstreamOnly,
		},
		{
			test:   "upstream_hidden_files",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
				{Header: &tar.Header{Name: "package/.npmrc", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictHiddenUpstreamOnly,
		},
		{
			test:   "rebuild_files",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
				{Header: &tar.Header{Name: "package/fileextra", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictRebuildOnly,
		},
		{
			test:   "content_diff",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 4, Mode: 0644}, Body: []byte("null")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictPackageJSONDiff,
		},
		{
			test:   "content_diff",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.tgz"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("{}")},
				{Header: &tar.Header{Name: "package/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("other")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictContentDiff,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			as := rebuild.NewFilesystemAssetStore(memfs.New())
			rb, up := rebuild.DebugRebuildAsset.For(tc.target), rebuild.DebugUpstreamAsset.For(tc.target)
			{
				w := must(as.Writer(context.Background(), rb))
				gw := gzip.NewWriter(w)
				tw := tar.NewWriter(gw)
				for _, entry := range tc.rebuild {
					orDie(entry.WriteTo(tw))
				}
				orDie(tw.Close())
				orDie(gw.Close())
			}
			{
				w := must(as.Writer(context.Background(), up))
				gw := gzip.NewWriter(w)
				tw := tar.NewWriter(gw)
				for _, entry := range tc.upstream {
					orDie(entry.WriteTo(tw))
				}
				orDie(tw.Close())
				orDie(gw.Close())
			}
			msg, err := Rebuilder{}.Compare(context.Background(), tc.target, rb, up, as, tc.inst)
			if err != nil {
				t.Errorf("Compare() = %v, want no error", err)
			}
			if msg != tc.expected {
				t.Errorf("Compare() = %v, want %v", msg, tc.expected)
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
