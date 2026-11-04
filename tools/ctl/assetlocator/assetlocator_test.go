// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package assetlocator

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"google.golang.org/api/option"
)

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

type mockDebugStore struct {
	rebuild.AssetStore
	reader *trackingReadCloser
}

func (m *mockDebugStore) Reader(ctx context.Context, a rebuild.Asset) (io.ReadCloser, error) {
	return m.reader, nil
}

func TestAssetStore_BuildInfoClosesReader(t *testing.T) {
	rc := &trackingReadCloser{
		Reader: bytes.NewBufferString(`{"id":"ob-123","BuildID":"b-456"}`),
	}
	s := &assetStore{
		metaAssetStore: &MetaAssetStore{},
		debug:          &mockDebugStore{reader: rc},
	}
	bi, err := s.buildInfo(context.Background(), rebuild.Target{Ecosystem: rebuild.Debian, Package: "main/foo", Version: "1.0", Artifact: "foo.deb"})
	if err != nil {
		t.Fatalf("buildInfo() error = %v", err)
	}
	if !rc.closed {
		t.Error("expected buildInfo() to close reader, but reader was left open")
	}
	if bi.ObliviousID != "ob-123" || bi.BuildID != "b-456" {
		t.Errorf("unexpected BuildInfo: %+v", bi)
	}
}

func TestAssetStore_BuildInfoInvalidJSON(t *testing.T) {
	rc := &trackingReadCloser{
		Reader: bytes.NewBufferString(`{invalid json`),
	}
	s := &assetStore{
		metaAssetStore: &MetaAssetStore{},
		debug:          &mockDebugStore{reader: rc},
	}
	_, err := s.buildInfo(context.Background(), rebuild.Target{Ecosystem: rebuild.Debian, Package: "main/foo", Version: "1.0", Artifact: "foo.deb"})
	if err == nil {
		t.Fatal("expected buildInfo() to return error on invalid JSON, got nil")
	}
	if !rc.closed {
		t.Error("expected buildInfo() to close reader on invalid JSON")
	}
}

func TestMetaAssetStore_ReusesGCSClient(t *testing.T) {
	ctx := context.WithValue(context.Background(), rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
	m := &MetaAssetStore{}
	c1, err := m.client(ctx)
	if err != nil {
		t.Fatalf("first client() error = %v", err)
	}
	defer c1.Close()
	c2, err := m.client(ctx)
	if err != nil {
		t.Fatalf("second client() error = %v", err)
	}
	if c1 != c2 {
		t.Error("expected MetaAssetStore.client() to return the same *gcs.Client instance")
	}
}
