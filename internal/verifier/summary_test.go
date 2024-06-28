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

package verifier

import (
	"archive/zip"
	"context"
	"crypto"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestSummarizeArtifacts(t *testing.T) {
	ctx := context.Background()
	t.Run("success", func(t *testing.T) {
		metadata := rebuild.NewFilesystemAssetStore(memfs.New())
		target := rebuild.Target{
			Ecosystem: rebuild.PyPI,
			Package:   "foo",
			Version:   "1.0.0",
			Artifact:  "foo-1.0.0.whl",
		}
		w, rebuildURI, err := metadata.Writer(ctx, rebuild.Asset{Target: target, Type: rebuild.RebuildAsset})
		orDie(err)
		origHash := hashext.NewMultiHash(crypto.SHA256)
		origZip := must(archivetest.ZipFile([]archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Comment: "foo"}, Body: []byte("data")},
		}))
		must(io.MultiWriter(w, origHash).Write(origZip.Bytes()))
		upstreamURI := "https://example.com/foo-1.0.0.whl"
		ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, &httpxtest.MockClient{
			Calls: []httpxtest.Call{
				{
					URL: upstreamURI,
					Response: &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(origZip),
					},
				},
			},
			URLValidator: func(expected, actual string) {
				if diff := cmp.Diff(expected, actual); diff != "" {
					t.Fatalf("URL mismatch (-want +got):\n%s", diff)
				}
			},
		})
		canonicalizedHash := hashext.NewMultiHash(crypto.SHA256)
		canonicalizedZip := must(archivetest.ZipFile([]archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Modified: time.UnixMilli(0)}, Body: []byte("data")},
		}))
		must(canonicalizedHash.Write(canonicalizedZip.Bytes()))
		rb, up, err := SummarizeArtifacts(ctx, metadata, target, upstreamURI, []crypto.Hash{crypto.SHA256})
		if err != nil {
			t.Fatalf("SummarizeArtifacts() returned error: %v", err)
		}
		if rb.URI != rebuildURI {
			t.Errorf("SummarizeArtifacts() returned diff for rb.URI: want %q, got %q", rebuildURI, rb.URI)
		}
		if diff := cmp.Diff(origHash.Sum(nil), rb.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(canonicalizedHash.Sum(nil), rb.CanonicalHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.CanonicalHash (-want +got):\n%s", diff)
		}
		if up.URI != upstreamURI {
			t.Errorf("SummarizeArtifacts() returned diff for up.URI: want %q, got %q", upstreamURI, up.URI)
		}
		if diff := cmp.Diff(origHash.Sum(nil), up.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(canonicalizedHash.Sum(nil), up.CanonicalHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.CanonicalHash (-want +got):\n%s", diff)
		}
	})
}

func must[T any](t T, err error) T {
	orDie(err)
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}
