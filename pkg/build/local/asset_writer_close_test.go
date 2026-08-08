// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var errLocalAssetClose = errors.New("asset close failed")

type localCloseErrorWriter struct{ bytes.Buffer }

func (*localCloseErrorWriter) Close() error { return errLocalAssetClose }

type localCloseErrorStore struct{}

func (localCloseErrorStore) Reader(context.Context, rebuild.Asset) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}

func (localCloseErrorStore) Writer(context.Context, rebuild.Asset) (io.WriteCloser, error) {
	return &localCloseErrorWriter{}, nil
}

func TestUploadHelpersReportCloseError(t *testing.T) {
	ctx := context.Background()
	store := localCloseErrorStore{}
	asset := rebuild.Asset{}
	filePath := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	buildExecutor := &DockerBuildExecutor{}
	runExecutor := &DockerRunExecutor{}
	tests := []struct {
		name string
		run  func() error
	}{
		{"docker build content", func() error {
			return buildExecutor.uploadContent(ctx, store, asset, []byte("data"))
		}},
		{"docker build file", func() error {
			return buildExecutor.uploadFile(ctx, store, asset, filePath)
		}},
		{"docker run content", func() error {
			return runExecutor.uploadContent(ctx, store, asset, []byte("data"))
		}},
		{"docker run file", func() error {
			return runExecutor.uploadFile(ctx, store, asset, filePath)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, errLocalAssetClose) {
				t.Fatalf("error = %v, want asset close error", err)
			}
		})
	}
}
