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

package pypi

import (
	"bytes"
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

func TestHTTPRegistry_Project(t *testing.T) {
	testCases := []struct {
		name         string
		pkg          string
		httpResponse *http.Response
		httpError    error
		expected     *Project
		expectedErr  error
		expectedURL  *url.URL
	}{
		{
			name: "Success",
			pkg:  "requests",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(bytes.NewReader([]byte(`{
                    "info": {
                        "name": "requests",
                        "version": "2.31.0"
                    },
                    "releases": {
                        "2.31.0": [
                            {"filename": "requests-2.31.0-py3-none-any.whl"}
                        ]
                    }
                }`))),
			},
			expectedURL: must(url.Parse("https://pypi.org/pypi/requests/json")),
			expected: &Project{
				Info: Info{
					Name:    "requests",
					Version: "2.31.0",
				},
				Releases: map[string][]Artifact{
					"2.31.0": {
						{Filename: "requests-2.31.0-py3-none-any.whl"},
					},
				},
			},
		},
		{
			name:        "HTTP Error",
			pkg:         "requests",
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
			expectedURL: must(url.Parse("https://pypi.org/pypi/requests/json")),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "nonexistent-pkg",
			httpResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("pypi registry error: Not Found"),
			expectedURL:  must(url.Parse("https://pypi.org/pypi/nonexistent-pkg/json")),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "bad-json-package",
			httpResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			expectedErr:  errors.New("invalid character ',' looking for beginning of object key string"),
			expectedURL:  must(url.Parse("https://pypi.org/pypi/bad-json-package/json")),
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
			actual, err := registry.Project(tc.pkg)
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

func TestHTTPRegistry_Release(t *testing.T) {
	testCases := []struct {
		name         string
		pkg          string
		version      string
		httpResponse *http.Response
		httpError    error
		expected     *Release
		expectedErr  error
		expectedURL  *url.URL
	}{
		{
			name:    "Success",
			pkg:     "requests",
			version: "2.31.0",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(bytes.NewReader([]byte(`{
                    "info": {
                        "name": "requests",
                        "version": "2.31.0"
                    },
                    "urls": [
                        {"filename": "requests-2.31.0-py3-none-any.whl"}
                    ]
                }`))),
			},
			expectedURL: must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
			expected: &Release{
				Info: Info{
					Name:    "requests",
					Version: "2.31.0",
				},
				Artifacts: []Artifact{
					{Filename: "requests-2.31.0-py3-none-any.whl"},
				},
			},
		},
		{
			name:        "HTTP Error",
			pkg:         "requests",
			version:     "2.31.0",
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
			expectedURL: must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "nonexistent-pkg",
			version:      "1.0.0",
			httpResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("pypi registry error: Not Found"),
			expectedURL:  must(url.Parse("https://pypi.org/pypi/nonexistent-pkg/1.0.0/json")),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "bad-json-pkg",
			version:      "1.0.0",
			httpResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			expectedErr:  errors.New("invalid character ',' looking for beginning of object key string"),
			expectedURL:  must(url.Parse("https://pypi.org/pypi/bad-json-pkg/1.0.0/json")),
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
			actual, err := registry.Release(tc.pkg, tc.version)
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
		name               string
		pkg                string
		version            string
		filename           string
		releaseHTTPResp    *http.Response
		artifactHTTPResp   *http.Response
		releaseURL         *url.URL
		artifactURL        *url.URL
		httpError          error
		expectedReadCloser io.ReadCloser
		expectedErr        error
	}{
		{
			name:     "Success",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "requests-2.31.0-py3-none-any.whl",
			releaseHTTPResp: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(bytes.NewReader([]byte(`{
                    "info": {"name": "requests", "version": "2.31.0"},
                    "urls": [
                        {"filename": "requests-2.31.0-py3-none-any.whl", "url": "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl"}
                    ]
                }`))),
			},
			artifactHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
			},
			releaseURL:         must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
			artifactURL:        must(url.Parse("https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl")),
			expectedReadCloser: io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
		},
		{
			name:        "Release Fetch Error",
			pkg:         "requests",
			version:     "2.31.0",
			releaseURL:  must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
		},
		{
			name:        "Release Fetch Error Status",
			pkg:         "error-package",
			version:     "1.0.0",
			filename:    "artifact.whl",
			releaseURL:  must(url.Parse("https://pypi.org/pypi/error-package/1.0.0/json")),
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
		},
		{
			name:     "Artifact Not Found",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "nonexistent-artifact.whl",
			releaseHTTPResp: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(bytes.NewReader([]byte(`{
                    "info": {"name": "requests", "version": "2.31.0"},
                    "urls": [
                        {"filename": "requests-2.31.0-py3-none-any.whl"}
                    ]
                }`))),
			},
			releaseURL:  must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
			artifactURL: must(url.Parse("https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl")),
			expectedErr: errors.New("not found"),
		},
		{
			name:     "Artifact Fetch Error Status",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "requests-2.31.0-py3-none-any.whl",
			releaseHTTPResp: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(bytes.NewReader([]byte(`{
                    "info": {"name": "requests", "version": "2.31.0"},
                    "urls": [
                        {"filename": "requests-2.31.0-py3-none-any.whl", "url": "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl"}
                    ]
                }`))),
			},
			artifactHTTPResp: &http.Response{StatusCode: 500, Status: http.StatusText(500)},
			releaseURL:       must(url.Parse("https://pypi.org/pypi/requests/2.31.0/json")),
			artifactURL:      must(url.Parse("https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl")),
			expectedErr:      errors.New("fetching artifact: Internal Server Error"),
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
							if diff := cmp.Diff(req.URL, tc.releaseURL); diff != "" {
								t.Errorf("URL mismatch: diff\n%v", diff)
							}
							return tc.releaseHTTPResp, tc.httpError
						}
						if diff := cmp.Diff(req.URL, tc.artifactURL); diff != "" {
							t.Errorf("URL mismatch: diff\n%v", diff)
						}
						return tc.artifactHTTPResp, tc.httpError
					},
				},
			}
			actual, err := registry.Artifact(tc.pkg, tc.version, tc.filename)
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
