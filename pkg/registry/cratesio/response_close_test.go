// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestHTTPRegistryClosesConsumedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		body string
		call func(HTTPRegistry) error
	}{
		{
			name: "crate metadata",
			url:  "https://crates.io/api/v1/crates/serde",
			body: `{"crate":{"id":"serde"},"versions":[]}`,
			call: func(r HTTPRegistry) error {
				_, err := r.Crate(context.Background(), "serde")
				return err
			},
		},
		{
			name: "version metadata",
			url:  "https://crates.io/api/v1/crates/serde/1.0.150",
			body: `{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download"}}`,
			call: func(r HTTPRegistry) error {
				_, err := r.Version(context.Background(), "serde", "1.0.150")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(tc.body)}
			client := &httpxtest.MockClient{
				Calls: []httpxtest.Call{{
					URL:      tc.url,
					Response: &http.Response{StatusCode: http.StatusOK, Body: body},
				}},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			if err := tc.call(HTTPRegistry{Client: client}); err != nil {
				t.Fatal(err)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestHTTPRegistryClosesFailedArtifactResponse(t *testing.T) {
	metadataBody := &closeTrackingBody{Reader: strings.NewReader(`{"version":{"num":"1.0.150","dl_path":"/api/v1/crates/serde/1.0.150/download"}}`)}
	artifactBody := &closeTrackingBody{Reader: strings.NewReader("error")}
	client := &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{
				URL:      "https://crates.io/api/v1/crates/serde/1.0.150",
				Response: &http.Response{StatusCode: http.StatusOK, Body: metadataBody},
			},
			{
				URL:      "https://crates.io/api/v1/crates/serde/1.0.150/download",
				Response: &http.Response{StatusCode: http.StatusInternalServerError, Status: "Internal Server Error", Body: artifactBody},
			},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}
	if _, err := (HTTPRegistry{Client: client}).Artifact(context.Background(), "serde", "1.0.150"); err == nil {
		t.Fatal("Artifact() returned nil error")
	}
	if !metadataBody.closed {
		t.Error("metadata response body was not closed")
	}
	if !artifactBody.closed {
		t.Error("artifact response body was not closed")
	}
}
