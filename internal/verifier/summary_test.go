// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"archive/zip"
	"context"
	"crypto"
	"io"
	"net/http"
	"slices"
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
		asset := rebuild.RebuildAsset.For(target)
		rebuildURI := metadata.URL(asset).String()
		w, err := metadata.Writer(ctx, asset)
		orDie(err)
		origHash := hashext.NewMultiHash(crypto.SHA256)
		origZip := must(archivetest.ZipFile([]archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Comment: "foo"}, Body: []byte("data")},
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/REBUILD"}, Body: []byte("data")},
		}))
		must(io.MultiWriter(w, origHash).Write(origZip.Bytes()))
		upstreamURI := "https://example.com/foo-1.0.0.whl"
		upstreamHash := hashext.NewMultiHash(crypto.SHA256)
		upstreamZip := must(archivetest.ZipFile([]archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Comment: "bar"}, Body: []byte("data")},
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/UPSTREAM"}, Body: []byte("data")},
		}))
		must(upstreamHash.Write(upstreamZip.Bytes()))
		ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, &httpxtest.MockClient{
			Calls: []httpxtest.Call{
				{
					URL: upstreamURI,
					Response: &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(upstreamZip),
					},
				},
			},
			URLValidator: httpxtest.NewURLValidator(t),
		})
		stabilizedHash := hashext.NewMultiHash(crypto.SHA256)
		stabilizedZip := must(archivetest.ZipFile([]archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "foo-0.0.1.dist-info/WHEEL", Modified: time.UnixMilli(0)}, Body: []byte("data")},
		}))
		must(stabilizedHash.Write(stabilizedZip.Bytes()))
		customStabilizers := must(archive.CreateCustomStabilizers([]archive.CustomStabilizerEntry{
			{Config: archive.CustomStabilizerConfigOneOf{ExcludePath: &archive.ExcludePath{Paths: []string{"**/REBUILD"}}}, Reason: "not supposed to be there"},
			{Config: archive.CustomStabilizerConfigOneOf{ExcludePath: &archive.ExcludePath{Paths: []string{"**/UPSTREAM"}}}, Reason: "not supposed to be there"},
		}, archive.ZipFormat))
		rb, up, err := SummarizeArtifacts(ctx, metadata, target, upstreamURI, []crypto.Hash{crypto.SHA256}, slices.Concat(archive.AllStabilizers, customStabilizers))
		if err != nil {
			t.Fatalf("SummarizeArtifacts() returned error: %v", err)
		}
		if rb.URI != rebuildURI {
			t.Errorf("SummarizeArtifacts() returned diff for rb.URI: want %q, got %q", rebuildURI, rb.URI)
		}
		if diff := cmp.Diff(origHash.Sum(nil), rb.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(stabilizedHash.Sum(nil), rb.StabilizedHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for rb.StabilizedHash (-want +got):\n%s", diff)
		}
		if up.URI != upstreamURI {
			t.Errorf("SummarizeArtifacts() returned diff for up.URI: want %q, got %q", upstreamURI, up.URI)
		}
		if diff := cmp.Diff(upstreamHash.Sum(nil), up.Hash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.Hash (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(stabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil)); diff != "" {
			t.Errorf("SummarizeArtifacts() returned diff for up.StabilizedHash (-want +got):\n%s", diff)
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
