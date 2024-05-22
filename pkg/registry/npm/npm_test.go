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

package npm

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

func TestHTTPRegistry_Package(t *testing.T) {
	testCases := []struct {
		name         string
		pkg          string
		httpResponse *http.Response
		httpError    error
		expected     *NPMPackage
		expectedErr  error
		expectedURI  *url.URL
	}{
		{
			name: "Success",
			pkg:  "express",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"versions":{"4.18.2":{"version":"4.18.2","repository":{"type":"git","url":"https://github.com/expressjs/express"}}}}`))),
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
			expectedURI: must(url.Parse("https://registry.npmjs.org/express")),
		},
		{
			name: "Legacy repository format",
			pkg:  "express",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"versions":{"4.18.2":{"version":"4.18.2","repository":"https://github.com/expressjs/express"}}}`))),
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
			expectedURI: must(url.Parse("https://registry.npmjs.org/express")),
		},
		{
			name:        "HTTP Error",
			pkg:         "express",
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
			expectedURI: must(url.Parse("https://registry.npmjs.org/express")),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "invalid-package",
			httpResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("npm registry error: Not Found"),
			expectedURI:  must(url.Parse("https://registry.npmjs.org/invalid-package")),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "bad-json-package",
			httpResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			expectedErr:  errors.New("invalid character ',' looking for beginning of object key string"),
			expectedURI:  must(url.Parse("https://registry.npmjs.org/bad-json-package")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := HTTPRegistry{
				Client: &fakeHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						if diff := cmp.Diff(req.URL, tc.expectedURI); diff != "" {
							t.Errorf("URI mismatch: diff\n%v", diff)
						}
						return tc.httpResponse, tc.httpError
					},
				},
			}
			actual, err := registry.Package(tc.pkg)
			if err != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if tc.expected != nil {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Package mismatch: diff\n%v", diff)
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
		httpResponse *http.Response
		httpError    error
		expected     *NPMVersion
		expectedErr  error
		expectedURI  *url.URL
	}{
		{
			name:    "Success",
			pkg:     "express",
			version: "4.18.2",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"version":"4.18.2","repository":{"type":"git","url":"https://github.com/expressjs/express"}}`))),
			},
			expected: &NPMVersion{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Version:    "4.18.2",
				Repository: Repository{Type: "git", URL: "https://github.com/expressjs/express"},
			},
			expectedURI: must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
		},
		{
			name:    "Legacy repository format",
			pkg:     "express",
			version: "4.18.2",
			httpResponse: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"version":"4.18.2","repository":"https://github.com/expressjs/express"}}`))),
			},
			expected: &NPMVersion{
				Name: "express",
				DistTags: DistTags{
					Latest: "4.18.2",
				},
				Version:    "4.18.2",
				Repository: Repository{URL: "https://github.com/expressjs/express"},
			},
			expectedURI: must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
		},
		{
			name:        "HTTP Error",
			pkg:         "express",
			version:     "4.18.2",
			httpError:   errors.New("network error"),
			expectedErr: errors.New("network error"),
			expectedURI: must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
		},
		{
			name:         "HTTP Error Status",
			pkg:          "invalid-package",
			version:      "1.0.0",
			httpResponse: &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:  errors.New("npm registry error: Not Found"),
			expectedURI:  must(url.Parse("https://registry.npmjs.org/invalid-package/1.0.0")),
		},
		{
			name:         "JSON Decode Error",
			pkg:          "bad-json-package",
			version:      "1.0.0",
			httpResponse: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"invalid": "json",,}`)))},
			expectedErr:  errors.New("invalid character ',' looking for beginning of object key string"),
			expectedURI:  must(url.Parse("https://registry.npmjs.org/bad-json-package/1.0.0")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := HTTPRegistry{
				Client: &fakeHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						if diff := cmp.Diff(req.URL, tc.expectedURI); diff != "" {
							t.Errorf("URI mismatch: diff\n%v", diff)
						}
						return tc.httpResponse, tc.httpError
					},
				},
			}
			actual, err := registry.Version(tc.pkg, tc.version)
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
		versionHTTPResp     *http.Response
		artifactHTTPResp    *http.Response
		httpError           error
		expectedReadCloser  io.ReadCloser
		expectedErr         error
		expectedVersionURI  *url.URL
		expectedArtifactURI *url.URL
	}{
		{
			name:    "Success",
			pkg:     "express",
			version: "4.18.2",
			versionHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"dist":{"tarball":"https://registry.npmjs.org/express/-/express-4.18.2.tgz"}}`))),
			},
			artifactHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
			},
			expectedReadCloser:  io.NopCloser(bytes.NewReader([]byte("This is the artifact content"))),
			expectedVersionURI:  must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
			expectedArtifactURI: must(url.Parse("https://registry.npmjs.org/express/-/express-4.18.2.tgz")),
		},
		{
			name:               "Version Fetch Error",
			pkg:                "express",
			version:            "4.18.2",
			httpError:          errors.New("network error"),
			expectedErr:        errors.New("network error"),
			expectedVersionURI: must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
		},
		{
			name:               "Version Fetch Error Status",
			pkg:                "error-package",
			version:            "1.0.0",
			versionHTTPResp:    &http.Response{StatusCode: 404, Status: http.StatusText(404)},
			expectedErr:        errors.New("npm registry error: Not Found"),
			expectedVersionURI: must(url.Parse("https://registry.npmjs.org/error-package/1.0.0")),
		},
		{
			name:    "Artifact Fetch Error",
			pkg:     "express",
			version: "4.18.2",
			versionHTTPResp: &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"name":"express","dist-tags":{"latest":"4.18.2"},"dist":{"tarball":"https://registry.npmjs.org/express/-/express-4.18.2.tgz"}}`))),
			},
			artifactHTTPResp:    &http.Response{StatusCode: 500, Status: http.StatusText(500)},
			expectedErr:         errors.New("fetching artifact: Internal Server Error"),
			expectedVersionURI:  must(url.Parse("https://registry.npmjs.org/express/4.18.2")),
			expectedArtifactURI: must(url.Parse("https://registry.npmjs.org/express/-/express-4.18.2.tgz")),
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
							if diff := cmp.Diff(req.URL, tc.expectedVersionURI); diff != "" {
								t.Errorf("URI mismatch: diff\n%v", diff)
							}
							return tc.versionHTTPResp, tc.httpError
						}
						if diff := cmp.Diff(req.URL, tc.expectedArtifactURI); diff != "" {
							t.Errorf("URI mismatch: diff\n%v", diff)
						}
						return tc.artifactHTTPResp, tc.httpError
					},
				},
			}
			actual, err := registry.Artifact(tc.pkg, tc.version)
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
