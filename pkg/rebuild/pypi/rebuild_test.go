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

package pypi

import (
	"archive/zip"
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
		rebuild  []*archive.ZipEntry
		upstream []*archive.ZipEntry
		inst     rebuild.Instructions
		expected error
	}{
		{
			test:   "success",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: nil,
		},
		{
			test:   "ds_store",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo/.DS_STORE"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictDSStore,
		},
		{
			test:   "crlf",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff\n")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff\r\n")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictLineEndings,
		},
		{
			test:   "mismatched_files",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file1"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("#b")},
				{FileHeader: &zip.FileHeader{Name: "foo/file2"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictMismatchedFiles,
		},
		{
			test:   "upstream_files",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
				{FileHeader: &zip.FileHeader{Name: "foo/fileextra"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictUpstreamOnly,
		},
		{
			test:   "rebuild_files",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
				{FileHeader: &zip.FileHeader{Name: "foo/fileextra"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictRebuildOnly,
		},
		{
			test:   "wheel_diff",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("###")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			inst:     rebuild.Instructions{},
			expected: verdictWheelDiff,
		},
		{
			test:   "content_diff",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "foo", Version: "0.0.1", Artifact: "foo-0.0.1-py3-none-any.whl"},
			rebuild: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("stuff")},
			},
			upstream: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL"}, Body: []byte("")},
				{FileHeader: &zip.FileHeader{Name: "foo/file"}, Body: []byte("other")},
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
				zw := zip.NewWriter(w)
				for _, entry := range tc.rebuild {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
			}
			{
				w := must(as.Writer(context.Background(), up))
				zw := zip.NewWriter(w)
				for _, entry := range tc.upstream {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
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
