// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cratesio

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
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: nil,
		},
		{
			test:   "ds_store",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.DS_STORE", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictDSStore,
		},
		{
			test:   "crlf",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 6, Mode: 0644}, Body: []byte("stuff\n")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 0, Mode: 0644}, Body: []byte("")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 7, Mode: 0644}, Body: []byte("stuff\r\n")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictLineEndings,
		},
		{
			test:   "git_only_diff",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"def"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "def"}},
			expected: verdictCargoVersionGit,
		},
		{
			test:   "normalized_toml_diff",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#b")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictCargoVersion,
		},
		{
			test:   "mismatched_files",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file1", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#b")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file2", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictMismatchedFiles,
		},
		{
			test:   "upstream_files",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
				{Header: &tar.Header{Name: "foo-0.0.1/fileextra", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictUpstreamOnly,
		},
		{
			test:   "rebuild_files",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
				{Header: &tar.Header{Name: "foo-0.0.1/fileextra", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
			expected: verdictRebuildOnly,
		},
		{
			test:   "content_diff",
			target: rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1.crate"},
			rebuild: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("stuff")},
			},
			upstream: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo-0.0.1/.cargo_vcs_info.json", Typeflag: tar.TypeReg, Size: 22, Mode: 0644}, Body: []byte(`{"git":{"sha1":"abc"}}`)},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/Cargo.toml.orig", Typeflag: tar.TypeReg, Size: 2, Mode: 0644}, Body: []byte("#a")},
				{Header: &tar.Header{Name: "foo-0.0.1/file", Typeflag: tar.TypeReg, Size: 5, Mode: 0644}, Body: []byte("other")},
			},
			inst:     rebuild.Instructions{Location: rebuild.Location{Ref: "abc"}},
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
