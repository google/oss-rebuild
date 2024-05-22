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
	"bytes"
	"context"
	"crypto"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
)

type mockHTTPClient struct {
	Reqests []*http.Request
	DoFunc  func(req *http.Request) (*http.Response, error)
}

func (c *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.Reqests = append(c.Reqests, req)
	return c.DoFunc(req)
}

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
		hash := hashext.NewMultiHash(crypto.SHA256)
		{
			zw := zip.NewWriter(io.MultiWriter(w, hash))
			ze := archive.ZipEntry{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Comment: "foo"}, Body: []byte("data")}
			orDie(ze.WriteTo(zw))
			orDie(zw.Close())
			orDie(w.Close())
		}
		upstreamURI := "https://example.com/foo-1.0.0.tgz"
		var c mockHTTPClient
		{
			upstreamZip := bytes.NewBuffer(nil)
			zw := zip.NewWriter(upstreamZip)
			ze := archive.ZipEntry{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Comment: "foo"}, Body: []byte("data")}
			orDie(ze.WriteTo(zw))
			orDie(zw.Close())
			c.DoFunc = func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(upstreamZip),
				}, nil
			}
		}
		ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, &c)
		canonicalized := hashext.NewMultiHash(crypto.SHA256)
		{
			zw := zip.NewWriter(canonicalized)
			ze := archive.ZipEntry{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Modified: time.UnixMilli(0)}, Body: []byte("data")}
			orDie(ze.WriteTo(zw))
			orDie(zw.Close())
		}
		rb, up, err := SummarizeArtifacts(ctx, metadata, target, upstreamURI, []crypto.Hash{crypto.SHA256})
		if err != nil {
			t.Fatalf("SummarizeArtifacts() returned error: %v", err)
		}
		if rb.URI != rebuildURI {
			t.Errorf("SummarizeArtifacts() returned diff for rb.URI: want %q, got %q", rebuildURI, rb.URI)
		}
		if diff := cmp.Diff(hash.Sum(nil), rb.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(canonicalized.Sum(nil), rb.CanonicalHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.CanonicalHash (-want +got):\n%s", diff)
		}
		if up.URI != upstreamURI {
			t.Errorf("SummarizeArtifacts() returned diff for up.URI: want %q, got %q", upstreamURI, up.URI)
		}
		if diff := cmp.Diff(hash.Sum(nil), up.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(canonicalized.Sum(nil), up.CanonicalHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.CanonicalHash (-want +got):\n%s", diff)
		}
		if len(c.Reqests) != 1 {
			t.Fatalf("SummarizeArtifacts() made %d requests, want 1", len(c.Reqests))
		}
		if c.Reqests[0].Method != http.MethodGet {
			t.Errorf("SummarizeArtifacts() made request with method %q, want %q", c.Reqests[0].Method, http.MethodGet)
		}
		if c.Reqests[0].URL.String() != upstreamURI {
			t.Errorf("SummarizeArtifacts() made request to %q, want %q", c.Reqests[0].URL.String(), upstreamURI)
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
