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

package cratesio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type fakeHTTPClient struct {
	DoFunc func(*http.Request) (*http.Response, error)
}

func (c *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.DoFunc(req)
}

func TestHTTPRegistry_Crate(t *testing.T) {
	testCases := []struct {
		name         string
		pkg          string
		expectedURL  *url.URL
		httpResponse *http.Response
		httpError    error
		expected     *Crate
		expectedErr  error
	}{
		{
			name:        "Success",
			pkg:         "serde",
			expectedURL: must(url.Parse("https://crates.io/api/v1/crates/serde")),
			httpResponse: &http.Response{
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
			name:        "HTTP Error",
			pkg:         "serde",
			expectedURL: must(url.Parse("https://crates.io/api/v1/crates/serde")),
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "nonexistent-pkg",
			expectedURL:  must(url.Parse("https://crates.io/api/v1/crates/nonexistent-pkg")),
			httpResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("crates.io registry error: Not Found"),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "bad-json-package",
			expectedURL:  must(url.Parse("https://crates.io/api/v1/crates/bad-json-package")),
			httpResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			expectedErr:  errors.New("invalid character ',' looking for beginning of object key string"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := HTTPRegistry{
				Client: &fakeHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						if diff := cmp.Diff(req.URL, tc.expectedURL); diff != "" {
							t.Errorf("URL mismatch: diff\n%v", diff)
						}
						return tc.httpResponse, tc.httpError
					},
				},
			}
			actual, err := registry.Crate(context.Background(), tc.pkg)
			if err != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Version mismatch: diff\n%v", diff)
				}
			}
		})
	}
}

func TestHTTPRegistry_Version(t *testing.T) {
	testCases := []struct {
		name         string
		pkg          string
		version      string
		expectedURL  *url.URL
		mockResponse *http.Response
		httpError    error
		expected     *CrateVersion
		expectedErr  error
	}{
		{
			name:        "Success",
			pkg:         "serde",
			version:     "1.0.150",
			expectedURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			mockResponse: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
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
			name:        "HTTP Error",
			pkg:         "serde",
			version:     "1.0.150",
			expectedURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "nonexistent-pkg",
			version:      "1.0.0",
			expectedURL:  must(url.Parse("https://crates.io/api/v1/crates/nonexistent-pkg/1.0.0")),
			mockResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("crates.io registry error: Not Found"),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "serde",
			version:      "1.0.150",
			expectedURL:  must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			mockResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json"}`)))},
			expectedErr:  errors.New("decoding error: invalid character 'i' looking for beginning of object key string"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := HTTPRegistry{
				Client: &fakeHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						if diff := cmp.Diff(req.URL, tc.expectedURL); diff != "" {
							t.Errorf("URL mismatch: diff\n%v", diff)
						}
						return tc.mockResponse, tc.httpError
					},
				},
			}
			actual, err := registry.Version(context.Background(), tc.pkg, tc.version)
			if err != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Version mismatch: diff\n%v", diff)
				}
			}
		})
	}
}

func TestHTTPRegistry_Artifact(t *testing.T) {
	testCases := []struct {
		name                string
		pkg                 string
		version             string
		expectedVersionURL  *url.URL
		versionHTTPResp     *http.Response
		expectedArtifactURL *url.URL
		artifacHTTPtResp    *http.Response
		httpError           error
		expectedReadCloser  io.ReadCloser
		expectedErr         error
	}{
		{
			name:               "Success",
			pkg:                "serde",
			version:            "1.0.150",
			expectedVersionURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			versionHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
			},
			expectedArtifactURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150/download")),
			artifacHTTPtResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
			},
			expectedReadCloser: io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
		},
		{
			name:               "Version Fetch Error",
			pkg:                "serde",
			version:            "1.0.150",
			expectedVersionURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			httpError:          errors.New("network error"),
			expectedErr:        errors.New("network error"),
		},
		{
			name:               "Version Fetch Error Status",
			pkg:                "nonexistent-pkg",
			version:            "1.0.0",
			expectedVersionURL: must(url.Parse("https://crates.io/api/v1/crates/nonexistent-pkg/1.0.0")),
			versionHTTPResp:    &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:        errors.New("crates.io registry error: Not Found"),
		},
		{
			name:               "Artifact Fetch Error",
			pkg:                "serde",
			version:            "1.0.150",
			expectedVersionURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150")),
			versionHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"version":{"num":"1.0.150", "dl_path":"/api/v1/crates/serde/1.0.150/download"}}`))),
			},
			expectedArtifactURL: must(url.Parse("https://crates.io/api/v1/crates/serde/1.0.150/download")),
			artifacHTTPtResp:    &http.Response{StatusCode: 500, Status: http.StatusText(500)},
			expectedErr:         errors.New("fetching artifact: Internal Server Error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			callCount := 0
			registry := HTTPRegistry{
				Client: &fakeHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						callCount++
						if callCount == 1 {
							if diff := cmp.Diff(req.URL, tc.expectedVersionURL); diff != "" {
								t.Errorf("URL mismatch: diff\n%v", diff)
							}
							return tc.versionHTTPResp, tc.httpError
						}
						if diff := cmp.Diff(req.URL, tc.expectedArtifactURL); diff != "" {
							t.Errorf("URL mismatch: diff\n%v", diff)
						}
						return tc.artifacHTTPtResp, tc.httpError
					},
				},
			}
			actual, err := registry.Artifact(context.Background(), tc.pkg, tc.version)
			if err != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expectedReadCloser != nil {
				if diff := cmp.Diff(must(io.ReadAll(actual)), must(io.ReadAll(tc.expectedReadCloser))); diff != "" {
					t.Errorf("Artifact content mismatch:\n%v", diff)
				}
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
