// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestHTTPRegistry_Crate(t *testing.T) {
	testCases := []struct {
		name        string
		pkg         string
		call        httpxtest.Call
		expected    *Crate
		expectedErr error
	}{
		{
			name: "Success",
			pkg:  "serde",
			call: httpxtest.Call{
				URL: "https://crates.io/api/v1/crates/serde",
				Response: &http.Response{
					StatusCode: 200,
					Body: io.NopCloser(bytes.NewReader([]byte(`{
                        "crate": {
                            "id": "serde",
                            "repository": "https://github.com/serde-rs/serde"
                        },
                        "versions": [
                            {"num": "1.0.150", "dl_path": "/api/v1/crates/serde/1.0.150/download"}
                        ]
                    }`))),
				},
			},
			expected: &Crate{
				Metadata: Metadata{
					Name:       "serde",
					Repository: "https://github.com/serde-rs/serde",
				},
				Versions: []Version{
					{
						Version:      "1.0.150",
						DownloadPath: "/api/v1/crates/serde/1.0.150/download",
						DownloadURL:  "https://crates.io/api/v1/crates/serde/1.0.150/download",
					},
				},
			},
		},
		{
			name: "HTTP Error",
			pkg:  "serde",
			call: httpxtest.Call{
				URL:   "https://crates.io/api/v1/crates/serde",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name: "HTTP Error Status",
			pkg:  "nonexistent-pkg",
			call: httpxtest.Call{
				URL:      "https://crates.io/api/v1/crates/nonexistent-pkg",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching crate metadata: Not Found"),
		},
		{
			name: "JSON Decode Error",
			pkg:  "bad-json-package",
			call: httpxtest.Call{
				URL:      "https://crates.io/api/v1/crates/bad-json-package",
				Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			},
			expectedErr: errors.New("invalid character ',' looking for beginning of object key string"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls:        []httpxtest.Call{tc.call},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			actual, err := HTTPRegistry{Client: mockClient}.Crate(context.Background(), tc.pkg)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Crate mismatch: diff\n%v", diff)
				}
			}
			if mockClient.CallCount() != 1 {
				t.Errorf("Expected 1 call, got %d", mockClient.CallCount())
			}
		})
	}
}

func TestHTTPRegistry_Version(t *testing.T) {
	testCases := []struct {
		name        string
		pkg         string
		version     string
		call        httpxtest.Call
		expected    *CrateVersion
		expectedErr error
	}{
		{
			name:    "Success",
			pkg:     "serde",
			version: "1.0.150",
			call: httpxtest.Call{URL: "https://crates.io/api/v1/crates/serde/1.0.150",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
				},
			},
			expected: &CrateVersion{
				Version: Version{
					Version:      "1.0.150",
					DownloadPath: "/api/v1/crates/serde/1.0.150/download",
					DownloadURL:  "https://crates.io/api/v1/crates/serde/1.0.150/download",
				},
			},
		},
		{
			name:    "HTTP Error",
			pkg:     "serde",
			version: "1.0.150",
			call: httpxtest.Call{URL: "https://crates.io/api/v1/crates/serde/1.0.150",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name:    "HTTP Error Status",
			pkg:     "nonexistent-pkg",
			version: "1.0.0",
			call: httpxtest.Call{URL: "https://crates.io/api/v1/crates/nonexistent-pkg/1.0.0",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching version: Not Found"),
		},
		{
			name:    "JSON Decode Error",
			pkg:     "serde",
			version: "1.0.150",
			call: httpxtest.Call{URL: "https://crates.io/api/v1/crates/serde/1.0.150",
				Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json"}`)))},
			},
			expectedErr: errors.New("decoding error: invalid character 'i' looking for beginning of object key string"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls:        []httpxtest.Call{tc.call},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			actual, err := HTTPRegistry{Client: mockClient}.Version(context.Background(), tc.pkg, tc.version)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Version mismatch: diff\n%v", diff)
				}
			}
			if mockClient.CallCount() != 1 {
				t.Errorf("Expected 1 call, got %d", mockClient.CallCount())
			}
		})
	}
}

func TestHTTPRegistry_Artifact(t *testing.T) {
	testCases := []struct {
		name               string
		pkg                string
		version            string
		calls              []httpxtest.Call
		expectedReadCloser io.ReadCloser
		expectedErr        error
	}{
		{
			name:    "Success",
			pkg:     "serde",
			version: "1.0.150",
			calls: []httpxtest.Call{
				{
					URL: "https://crates.io/api/v1/crates/serde/1.0.150",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
					},
				},
				{
					URL: "https://crates.io/api/v1/crates/serde/1.0.150/download",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
					},
				},
			},
			expectedReadCloser: io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
		},
		{
			name:    "Version Fetch Error",
			pkg:     "serde",
			version: "1.0.150",
			calls: []httpxtest.Call{
				{
					URL:   "https://crates.io/api/v1/crates/serde/1.0.150",
					Error: errors.New("network error"),
				},
			},
		},
		{
			name:    "Version Fetch Error Status",
			pkg:     "nonexistent-pkg",
			version: "1.0.0",
			calls: []httpxtest.Call{
				{
					URL:      "https://crates.io/api/v1/crates/nonexistent-pkg/1.0.0",
					Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
				},
			},
			expectedErr: errors.New("fetching version: Not Found"),
		},
		{
			name:    "Artifact Fetch Error",
			pkg:     "serde",
			version: "1.0.150",
			calls: []httpxtest.Call{
				{
					URL: "https://crates.io/api/v1/crates/serde/1.0.150",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
					},
				},
				{
					URL:      "https://crates.io/api/v1/crates/serde/1.0.150/download",
					Response: &http.Response{StatusCode: 500, Status: http.StatusText(500)},
				},
			},
			expectedErr: errors.New("fetching artifact: Internal Server Error"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls:        tc.calls,
				URLValidator: httpxtest.NewURLValidator(t),
			}
			actual, err := HTTPRegistry{Client: mockClient}.Artifact(context.Background(), tc.pkg, tc.version)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expectedReadCloser != nil {
				if diff := cmp.Diff(must(io.ReadAll(actual)), must(io.ReadAll(tc.expectedReadCloser))); diff != "" {
					t.Errorf("Artifact content mismatch:\n%v", diff)
				}
			}
			if mockClient.CallCount() != len(tc.calls) {
				t.Errorf("Expected %d calls, got %d", len(tc.calls), mockClient.CallCount())
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
