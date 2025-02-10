// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

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

func TestHTTPRegistry_Package(t *testing.T) {
	testCases := []struct {
		name        string
		pkg         string
		call        httpxtest.Call
		expected    *NPMPackage
		expectedErr error
	}{
		{
			name: "Success",
			pkg:  "express",
			call: httpxtest.Call{
				URL: "https://registry.npmjs.org/express",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"versions":{"4.18.2":{"version":"4.18.2","repository":{"type":"git","url":"https://github.com/expressjs/express"}}}}`))),
				},
			},
			expected: &NPMPackage{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Versions: map[string]Release{
					"4.18.2": {
						Version:    "4.18.2",
						Repository: Repository{Type: "git", URL: "https://github.com/expressjs/express"},
					},
				},
			},
		},
		{
			name: "Legacy repository format",
			pkg:  "express",
			call: httpxtest.Call{
				URL: "https://registry.npmjs.org/express",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"versions":{"4.18.2":{"version":"4.18.2","repository":"https://github.com/expressjs/express"}}}`))),
				},
			},
			expected: &NPMPackage{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Versions: map[string]Release{
					"4.18.2": {
						Version:    "4.18.2",
						Repository: Repository{URL: "https://github.com/expressjs/express"},
					},
				},
			},
		},
		{
			name: "HTTP Error",
			pkg:  "express",
			call: httpxtest.Call{
				URL:   "https://registry.npmjs.org/express",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name: "HTTP Error Status",
			pkg:  "invalid-package",
			call: httpxtest.Call{
				URL:      "https://registry.npmjs.org/invalid-package",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching package: Not Found"),
		},
		{
			name: "JSON Decode Error",
			pkg:  "bad-json-package",
			call: httpxtest.Call{
				URL:      "https://registry.npmjs.org/bad-json-package",
				Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			},
			expectedErr: errors.New("invalid character ',' looking for beginning of object key string"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls: []httpxtest.Call{tc.call},
				URLValidator: func(expected, actual string) {
					if diff := cmp.Diff(expected, actual); diff != "" {
						t.Fatalf("URL mismatch (-want +got):\n%s", diff)
					}
				},
			}
			actual, err := HTTPRegistry{Client: mockClient}.Package(context.Background(), tc.pkg)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Package mismatch: diff\n%v", diff)
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
		expected    *NPMVersion
		expectedErr error
	}{
		{
			name:    "Success",
			pkg:     "express",
			version: "4.18.2",
			call: httpxtest.Call{
				URL: "https://registry.npmjs.org/express/4.18.2",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"version":"4.18.2","repository":{"type":"git","url":"https://github.com/expressjs/express"}}`))),
				},
			},
			expected: &NPMVersion{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Version:    "4.18.2",
				Repository: Repository{Type: "git", URL: "https://github.com/expressjs/express"},
			},
		},
		{
			name:    "Legacy repository format",
			pkg:     "express",
			version: "4.18.2",
			call: httpxtest.Call{
				URL: "https://registry.npmjs.org/express/4.18.2",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"version":"4.18.2","repository":"https://github.com/expressjs/express"}`))),
				},
			},
			expected: &NPMVersion{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Version:    "4.18.2",
				Repository: Repository{URL: "https://github.com/expressjs/express"},
			},
		},
		{
			name:    "HTTP Error",
			pkg:     "express",
			version: "4.18.2",
			call: httpxtest.Call{
				URL:   "https://registry.npmjs.org/express/4.18.2",
				Error: errors.New("network error"),
			},
			expectedErr: errors.New("network error"),
		},
		{
			name:    "HTTP Error Status",
			pkg:     "invalid-package",
			version: "1.0.0",
			call: httpxtest.Call{
				URL:      "https://registry.npmjs.org/invalid-package/1.0.0",
				Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			},
			expectedErr: errors.New("fetching version: Not Found"),
		},
		{
			name:    "JSON Decode Error",
			pkg:     "bad-json-package",
			version: "1.0.0",
			call: httpxtest.Call{
				URL:      "https://registry.npmjs.org/bad-json-package/1.0.0",
				Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			},
			expectedErr: errors.New("invalid character ',' looking for beginning of object key string"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls: []httpxtest.Call{tc.call},
				URLValidator: func(expected, actual string) {
					if diff := cmp.Diff(expected, actual); diff != "" {
						t.Fatalf("URL mismatch (-want +got):\n%s", diff)
					}
				},
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
			pkg:     "express",
			version: "4.18.2",
			calls: []httpxtest.Call{
				{
					URL: "https://registry.npmjs.org/express/4.18.2",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"dist":{"tarball":"https://registry.npmjs.org/express/-/express-4.18.2.tgz"}}`))),
					},
				},
				{
					URL: "https://registry.npmjs.org/express/-/express-4.18.2.tgz",
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
			pkg:     "express",
			version: "4.18.2",
			calls: []httpxtest.Call{
				{
					URL:   "https://registry.npmjs.org/express/4.18.2",
					Error: errors.New("network error"),
				},
			},
			expectedErr: errors.New("network error"),
		},
		{
			name:    "Version Fetch Error Status",
			pkg:     "error-package",
			version: "1.0.0",
			calls: []httpxtest.Call{
				{
					URL:      "https://registry.npmjs.org/error-package/1.0.0",
					Response: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
				},
			},
			expectedErr: errors.New("fetching version: Not Found"),
		},
		{
			name:    "Artifact Fetch Error",
			pkg:     "express",
			version: "4.18.2",
			calls: []httpxtest.Call{
				{
					URL: "https://registry.npmjs.org/express/4.18.2",
					Response: &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"dist":{"tarball":"https://registry.npmjs.org/express/-/express-4.18.2.tgz"}}`))),
					},
				},
				{
					URL:      "https://registry.npmjs.org/express/-/express-4.18.2.tgz",
					Response: &http.Response{StatusCode: 500, Status: http.StatusText(500)},
				},
			},
			expectedErr: errors.New("fetching artifact: Internal Server Error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls: tc.calls,
				URLValidator: func(expected, actual string) {
					if diff := cmp.Diff(expected, actual); diff != "" {
						t.Fatalf("URL mismatch (-want +got):\n%s", diff)
					}
				},
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
