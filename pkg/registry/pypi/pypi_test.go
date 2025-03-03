// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

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

func TestHTTPRegistry_Project(t *testing.T) {
	testCases := []struct {
		name        string
		pkg         string
		call        httpxtest.Call
		expected    *Project
		expectedErr error
	}{
		{
			name: "Success",
			pkg:  "requests",
			call: httpxtest.Call{
				URL: "https://pypi.org/pypi/requests/json",
				Response: &http.Response{
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
			},
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
			name: "HTTP Error",
			pkg:  "requests",
			call: httpxtest.Call{
				URL:   "https://pypi.org/pypi/requests/json",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name: "HTTP Error Status",
			pkg:  "nonexistent-pkg",
			call: httpxtest.Call{
				URL:      "https://pypi.org/pypi/nonexistent-pkg/json",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching project: Not Found"),
		},
		{
			name: "JSON Decode Error",
			pkg:  "bad-json-package",
			call: httpxtest.Call{
				URL:      "https://pypi.org/pypi/bad-json-package/json",
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
			actual, err := HTTPRegistry{Client: mockClient}.Project(context.Background(), tc.pkg)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Project mismatch: diff\n%v", diff)
				}
			}
			if mockClient.CallCount() != 1 {
				t.Errorf("Expected 1 call, got %d", mockClient.CallCount())
			}
		})
	}
}

func TestHTTPRegistry_Release(t *testing.T) {
	testCases := []struct {
		name        string
		pkg         string
		version     string
		call        httpxtest.Call
		expected    *Release
		expectedErr error
	}{
		{
			name:    "Success",
			pkg:     "requests",
			version: "2.31.0",
			call: httpxtest.Call{
				URL: "https://pypi.org/pypi/requests/2.31.0/json",
				Response: &http.Response{
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
			},
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
			name:    "HTTP Error",
			pkg:     "requests",
			version: "2.31.0",
			call: httpxtest.Call{
				URL:   "https://pypi.org/pypi/requests/2.31.0/json",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name:    "HTTP Error Status",
			pkg:     "nonexistent-pkg",
			version: "1.0.0",
			call: httpxtest.Call{
				URL:      "https://pypi.org/pypi/nonexistent-pkg/1.0.0/json",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching release: Not Found"),
		},
		{
			name:    "JSON Decode Error",
			pkg:     "bad-json-pkg",
			version: "1.0.0",
			call: httpxtest.Call{
				URL:      "https://pypi.org/pypi/bad-json-pkg/1.0.0/json",
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
			actual, err := HTTPRegistry{Client: mockClient}.Release(context.Background(), tc.pkg, tc.version)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Release mismatch: diff\n%v", diff)
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
		filename           string
		calls              []httpxtest.Call
		expectedReadCloser io.ReadCloser
		expectedErr        error
	}{
		{
			name:     "Success",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "requests-2.31.0-py3-none-any.whl",
			calls: []httpxtest.Call{
				{
					URL: "https://pypi.org/pypi/requests/2.31.0/json",
					Response: &http.Response{
						StatusCode: 200,
						Body: io.NopCloser(bytes.NewReader([]byte(`{
                            "info": {"name": "requests", "version": "2.31.0"},
                            "urls": [
                                {"filename": "requests-2.31.0-py3-none-any.whl", "url": "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl"}
                            ]
                        }`))),
					},
				},
				{
					URL: "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
					},
				},
			},
			expectedReadCloser: io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
		},
		{
			name:     "Release Fetch Error",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "requests-2.31.0-py3-none-any.whl",
			calls: []httpxtest.Call{
				{
					URL:   "https://pypi.org/pypi/requests/2.31.0/json",
					Error: errors.New("network error"),
				},
			},
			expectedErr: errors.New("network error"),
		},
		{
			name:     "Artifact Not Found",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "nonexistent-artifact.whl",
			calls: []httpxtest.Call{
				{
					URL: "https://pypi.org/pypi/requests/2.31.0/json",
					Response: &http.Response{
						StatusCode: 200,
						Body: io.NopCloser(bytes.NewReader([]byte(`{
                            "info": {"name": "requests", "version": "2.31.0"},
                            "urls": [
                                {"filename": "requests-2.31.0-py3-none-any.whl"}
                            ]
                        }`))),
					},
				},
			},
			expectedErr: errors.New("not found"),
		},
		{
			name:     "Artifact Fetch Error",
			pkg:      "requests",
			version:  "2.31.0",
			filename: "requests-2.31.0-py3-none-any.whl",
			calls: []httpxtest.Call{
				{
					URL: "https://pypi.org/pypi/requests/2.31.0/json",
					Response: &http.Response{
						StatusCode: 200,
						Body: io.NopCloser(bytes.NewReader([]byte(`{
                            "info": {"name": "requests", "version": "2.31.0"},
                            "urls": [
                                {"filename": "requests-2.31.0-py3-none-any.whl", "url": "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl"}
                            ]
                        }`))),
					},
				},
				{
					URL:      "https://files.pythonhosted.org/packages/00/00/00000000/requests-2.31.0-py3-none-any.whl",
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
			actual, err := HTTPRegistry{Client: mockClient}.Artifact(context.Background(), tc.pkg, tc.version, tc.filename)
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
