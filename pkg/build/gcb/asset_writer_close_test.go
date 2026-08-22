// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var errAssetClose = errors.New("asset close failed")

type closeErrorWriter struct{ bytes.Buffer }

func (*closeErrorWriter) Close() error { return errAssetClose }

type closeErrorStore struct{}

func (closeErrorStore) Reader(context.Context, rebuild.Asset) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}

func (closeErrorStore) Writer(context.Context, rebuild.Asset) (io.WriteCloser, error) {
	return &closeErrorWriter{}, nil
}

func TestUploadHelpersReportCloseError(t *testing.T) {
	e := &Executor{}
	tests := []struct {
		name string
		run  func() error
	}{
		{"content", func() error {
			return e.uploadContent(context.Background(), closeErrorStore{}, rebuild.Asset{}, []byte("data"))
		}},
		{"build info", func() error {
			return e.uploadBuildInfo(context.Background(), closeErrorStore{}, rebuild.Asset{}, rebuild.BuildInfo{})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, errAssetClose) {
				t.Fatalf("error = %v, want asset close error", err)
			}
		})
	}
}
