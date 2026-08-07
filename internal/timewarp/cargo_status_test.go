// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestAddPackageToIndexRejectsUpstreamServerError(t *testing.T) {
	client := &httpxtest.MockClient{
		Calls: []httpxtest.Call{{
			Response: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewReader(nil)),
			},
		}},
		SkipURLValidation: true,
	}
	err := (Handler{Client: client}).addPackageToIndex(memfs.New(), "0123456789abcdef", "foo")
	if err == nil {
		t.Fatal("addPackageToIndex() error = nil for upstream HTTP 500")
	}
}

func TestAddPackageToIndexSkipsMissingPackage(t *testing.T) {
	client := &httpxtest.MockClient{
		Calls: []httpxtest.Call{{
			Response: &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewReader(nil)),
			},
		}},
		SkipURLValidation: true,
	}
	if err := (Handler{Client: client}).addPackageToIndex(memfs.New(), "0123456789abcdef", "foo"); err != nil {
		t.Fatalf("addPackageToIndex() error = %v for upstream HTTP 404", err)
	}
}
