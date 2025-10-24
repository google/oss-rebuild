// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestHandler_ServeHTTP(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		basicAuth string
		headers   map[string]string
		client    *httpxtest.MockClient
		want      *http.Response
	}{
		{
			name:      "npm package request - successful time warp",
			url:       "http://localhost:8081/some-package",
			basicAuth: "npm:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://registry.npmjs.org/some-package",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"_id": "some-package",
								"time": {
									"created": "2021-01-01T00:00:00Z",
									"modified": "2023-01-01T00:00:00Z",
									"1.0.0": "2021-06-01T00:00:00Z",
									"2.0.0": "2022-06-01T00:00:00Z"
								},
								"versions": {
									"1.0.0": {
										"version": "1.0.0",
										"description": "v1 desc",
										"repository": "repo1"
									},
									"2.0.0": {
										"version": "2.0.0",
										"description": "v2 desc",
										"repository": "repo2"
									}
								}
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"_id": "some-package",
					"time": {
						"created": "2021-01-01T00:00:00Z",
						"modified": "2021-06-01T00:00:00Z",
						"1.0.0": "2021-06-01T00:00:00Z"
					},
					"versions": {
						"1.0.0": {
							"version": "1.0.0",
							"description": "v1 desc",
							"repository": "repo1"
						}
					},
					"description": "v1 desc",
					"repository": "repo1",
					"dist-tags": {
						"latest": "1.0.0"
					}
				}`)),
			},
		},
		{
			name:      "npm package request with org - successful time warp",
			url:       "http://localhost:8081/@org/some-package",
			basicAuth: "npm:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://registry.npmjs.org/@org/some-package",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"_id": "@org/some-package",
								"time": {
									"created": "2021-01-01T00:00:00Z",
									"modified": "2023-01-01T00:00:00Z",
									"1.0.0": "2021-06-01T00:00:00Z",
									"2.0.0": "2022-06-01T00:00:00Z"
								},
								"versions": {
									"1.0.0": {
										"version": "1.0.0",
										"description": "v1 desc",
										"repository": "repo1"
									},
									"2.0.0": {
										"version": "2.0.0",
										"description": "v2 desc",
										"repository": "repo2"
									}
								}
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"_id": "@org/some-package",
					"time": {
						"created": "2021-01-01T00:00:00Z",
						"modified": "2021-06-01T00:00:00Z",
						"1.0.0": "2021-06-01T00:00:00Z"
					},
					"versions": {
						"1.0.0": {
							"version": "1.0.0",
							"description": "v1 desc",
							"repository": "repo1"
						}
					},
					"description": "v1 desc",
					"repository": "repo1",
					"dist-tags": {
						"latest": "1.0.0"
					}
				}`)),
			},
		},
		{
			name:      "npm version request - skipped time warp",
			url:       "http://localhost:8081/some-package/2.0.0",
			basicAuth: "npm:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls:        []httpxtest.Call{},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`<a href="https://registry.npmjs.org/some-package/2.0.0">Found</a>.

`)),
			},
		},
		{
			name:      "npm search request - skipped time warp",
			url:       "http://localhost:8081/-/v1/search?text=some-package",
			basicAuth: "npm:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls:        []httpxtest.Call{},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`<a href="https://registry.npmjs.org/-/v1/search?text=some-package">Found</a>.

`)),
			},
		},
		{
			name:      "pypi project request - successful time warp",
			url:       "http://localhost:8081/pypi/some-package/json",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://pypi.org/pypi/some-package/json",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"info": {
									"name": "some-package",
									"version": "2.0.0",
									"requires_dist": ["req1", "req2", "req3"]
								},
								"releases": {
									"1.0.0": [
										{
											"upload_time_iso_8601": "2021-06-01T00:00:00Z",
											"filename": "some-package-1.0.0.tar.gz"
										}
									],
									"2.0.0": [
										{
											"upload_time_iso_8601": "2022-06-01T00:00:00Z",
											"filename": "some-package-2.0.0.tar.gz"
										}
									]
								}
							}`)),
						},
					},
					{
						Method: "GET",
						URL:    "https://pypi.org/pypi/some-package/1.0.0/json",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"info": {
									"name": "some-package",
									"version": "1.0.0",
									"requires_dist": ["req1", "req2"]
								}
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"info": {
						"name": "some-package",
						"version": "1.0.0",
						"requires_dist": ["req1", "req2"]
					},
					"releases": {
						"1.0.0": [
							{
								"upload_time_iso_8601": "2021-06-01T00:00:00Z",
								"filename": "some-package-1.0.0.tar.gz"
							}
						]
					}
				}`)),
			},
		},
		{
			name:      "pypi project request - timewarp but no available packages",
			url:       "http://localhost:8081/pypi/some-package/json",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://pypi.org/pypi/some-package/json",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"info": {
									"name": "some-package",
									"version": "2.0.0",
									"requires_dist": ["req1", "req2", "req3"]
								},
								"releases": {
									"1.0.0": [
										{
											"upload_time_iso_8601": "2023-06-01T00:00:00Z",
											"filename": "some-package-1.0.0.tar.gz"
										}
									],
									"2.0.0": [
										{
											"upload_time_iso_8601": "2024-06-01T00:00:00Z",
											"filename": "some-package-2.0.0.tar.gz"
										}
									]
								}
							}`)),
						},
					},
					{
						Method: "GET",
						URL:    "https://pypi.org/pypi/some-package/json",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"info": {
									"name": "some-package",
									"version": "1.0.0",
									"requires_dist": ["req1", "req2"]
								}
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"info": {
						"name": "some-package",
						"version": "1.0.0",
						"requires_dist": ["req1", "req2"]
					},
					"releases": {}
				}`)),
			},
		},
		{
			name:      "pypi version request - skipped time warp",
			url:       "http://localhost:8081/pypi/some-package/2.0.0/json",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls:        []httpxtest.Call{},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`<a href="https://pypi.org/pypi/some-package/2.0.0/json">Found</a>.

`)),
			},
		},
		{
			name:      "pypi simple project request - successful time warp",
			url:       "http://localhost:8081/simple/some-package/",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://pypi.org/simple/some-package/",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"name": "some-package",
								"files": [
									{
										"filename": "some-package-0.9.0.tar.gz",
										"upload-time": "2021-01-01T00:00:00.123456Z",
										"yanked": true
									},
									{
										"filename": "some-package-1.0.0.tar.gz",
										"upload-time": "2021-06-01T00:00:00.123456Z",
										"yanked": false
									},
									{
										"filename": "some-package-1.0.0-py3-none-any.whl",
										"upload-time": "2021-06-02T00:00:00.123456Z",
										"yanked": false
									},
									{
										"filename": "some-package-2.0.0.tar.gz",
										"upload-time": "2022-06-01T00:00:00.123456Z",
										"yanked": false
									}
								],
								"versions": ["0.9.0", "1.0.0", "2.0.0"]
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"name": "some-package",
					"files": [
						{
							"filename": "some-package-0.9.0.tar.gz",
							"upload-time": "2021-01-01T00:00:00.123456Z",
							"yanked": true
						},
						{
							"filename": "some-package-1.0.0.tar.gz",
							"upload-time": "2021-06-01T00:00:00.123456Z",
							"yanked": false
						},
						{
							"filename": "some-package-1.0.0-py3-none-any.whl",
							"upload-time": "2021-06-02T00:00:00.123456Z",
							"yanked": false
						}
					],
					"versions": ["0.9.0", "1.0.0"]
				}`)),
			},
		},
		{
			name:      "pypi simple project request - timewarp but no available files",
			url:       "http://localhost:8081/simple/some-other-package/",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://pypi.org/simple/some-other-package/",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Type": []string{"application/json"},
							},
							Body: io.NopCloser(bytes.NewBufferString(`{
								"name": "some-other-package",
								"files": [
									{
										"filename": "some-other-package-1.0.0.tar.gz",
										"upload-time": "2023-01-01T00:00:00.000000Z",
										"yanked": false
									}
								],
								"versions": ["1.0.0"]
							}`)),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"name": "some-other-package",
					"files": null,
					"versions": null
				}`)),
			},
		},
		{
			name:      "pypi simple file request - skipped time warp",
			url:       "http://localhost:8081/simple/some-package/some-package-1.0.0.tar.gz",
			basicAuth: "pypi:2022-01-01T00:00:00Z",
			client: &httpxtest.MockClient{
				Calls:        []httpxtest.Call{},
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`<a href="https://pypi.org/simple/some-package/some-package-1.0.0.tar.gz">Found</a>.

`)),
			},
		},
		{
			name:      "invalid platform",
			url:       "http://localhost:8081/some-package",
			basicAuth: "invalid:2022-01-01T00:00:00Z",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("unsupported platform\n")),
			},
		},
		{
			name:      "invalid time format",
			url:       "http://localhost:8081/some-package",
			basicAuth: "npm:invalid-time",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("invalid time set\n")),
			},
		},
		{
			name:      "time too far in past",
			url:       "http://localhost:8081/some-package",
			basicAuth: "npm:1999-01-01T00:00:00Z",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("time set too far in the past\n")),
			},
		},
		{
			name:      "cargosparse config.json request",
			url:       "http://localhost:8081/config.json",
			basicAuth: "cargosparse:abc1234",
			client: &httpxtest.MockClient{
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"dl": "https://static.crates.io/crates","api": "/"}`)),
			},
		},
		{
			name:      "cargosparse index file request",
			url:       "http://localhost:8081/so/me/some-crate",
			basicAuth: "cargosparse:abc1234",
			client: &httpxtest.MockClient{
				URLValidator: httpxtest.NewURLValidator(t),
			},
			want: &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`<a href="https://raw.githubusercontent.com/rust-lang/crates.io-index/abc1234/so/me/some-crate">Found</a>.

`)),
			},
		},
		{
			name:      "cargosparse missing commit hash",
			url:       "http://localhost:8081/some-crate",
			basicAuth: "cargosparse:",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("no commit hash set\n")),
			},
		},
		{
			name:      "cargosparse invalid commit hash",
			url:       "http://localhost:8081/some-crate",
			basicAuth: "cargosparse:invalid-hash!",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("invalid commit hash format\n")),
			},
		},
		{
			name:      "cargogitarchive successful request",
			url:       "http://localhost:8081/index.git.tar",
			basicAuth: "cargogitarchive:abc1234",
			headers: map[string]string{
				"X-Package-Names": "serde,tokio,clap",
			},
			client: &httpxtest.MockClient{
				URLValidator: httpxtest.NewURLValidator(t),
				Calls: []httpxtest.Call{
					{
						Method: "GET",
						URL:    "https://raw.githubusercontent.com/rust-lang/crates.io-index/abc1234/se/rd/serde",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString(`{"name":"serde","vers":"1.0.0"}`)),
						},
					},
					{
						Method: "GET",
						URL:    "https://raw.githubusercontent.com/rust-lang/crates.io-index/abc1234/to/ki/tokio",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString(`{"name":"tokio","vers":"1.0.0"}`)),
						},
					},
					{
						Method: "GET",
						URL:    "https://raw.githubusercontent.com/rust-lang/crates.io-index/abc1234/cl/ap/clap",
						Response: &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString(`{"name":"clap","vers":"4.0.0"}`)),
						},
					},
				},
			},
			want: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/x-tar"},
				},
				// Body content will be a tar archive - we'll validate it's not empty
			},
		},
		{
			name:      "cargogitarchive invalid path",
			url:       "http://localhost:8081/wrong-path",
			basicAuth: "cargogitarchive:abc1234",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("invalid path for cargogitarchive\n")),
			},
		},
		{
			name:      "cargogitarchive missing package names header",
			url:       "http://localhost:8081/index.git.tar",
			basicAuth: "cargogitarchive:abc1234",
			client:    &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("missing X-Package-Names header\n")),
			},
		},
		{
			name:      "cargogitarchive missing commit hash",
			url:       "http://localhost:8081/index.git.tar",
			basicAuth: "cargogitarchive:",
			headers: map[string]string{
				"X-Package-Names": "serde",
			},
			client: &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("no commit hash set\n")),
			},
		},
		{
			name:      "cargogitarchive invalid commit hash",
			url:       "http://localhost:8081/index.git.tar",
			basicAuth: "cargogitarchive:invalid-hash!",
			headers: map[string]string{
				"X-Package-Names": "serde",
			},
			client: &httpxtest.MockClient{},
			want: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString("invalid commit hash format\n")),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			handler := &Handler{Client: tt.client}
			req := httptest.NewRequest("GET", tt.url, nil)
			if tt.basicAuth != "" {
				parts := bytes.SplitN([]byte(tt.basicAuth), []byte(":"), 2)
				req.SetBasicAuth(string(parts[0]), string(parts[1]))
			}
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			// Execute
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			got := rr.Result()

			// Compare status codes
			if diff := cmp.Diff(tt.want.StatusCode, got.StatusCode); diff != "" {
				t.Errorf("handler returned wrong status code (-want +got):\n%s", diff)
			}
			// Compare headers
			if got.StatusCode == http.StatusOK {
				for k, v := range tt.want.Header {
					if diff := cmp.Diff(v, got.Header[k]); diff != "" {
						t.Errorf("handler returned wrong header for %s (-want +got):\n%s", k, diff)
					}
				}
			}
			// Compare bodies
			gotBody, err := io.ReadAll(got.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}
			var wantBody []byte
			if tt.want.Body != nil {
				wantBody, err = io.ReadAll(tt.want.Body)
				if err != nil {
					t.Fatalf("Failed to read expected body: %v", err)
				}
			}
			// Compare responses
			if got.Header.Get("Content-Type") == "application/json" {
				var gotJSON, wantJSON any
				if err := json.Unmarshal(gotBody, &gotJSON); err != nil {
					t.Fatalf("Failed to parse response body: %v", err)
				}
				if err := json.Unmarshal(wantBody, &wantJSON); err != nil {
					t.Fatalf("Failed to parse expected body: %v", err)
				}
				if diff := cmp.Diff(wantJSON, gotJSON); diff != "" {
					t.Errorf("handler returned unexpected body (-want +got):\n%s", diff)
				}
			} else if got.Header.Get("Content-Type") == "application/x-tar" {
				// For tar archives, just verify that we got some content and it has tar header
				if len(gotBody) == 0 {
					t.Error("expected tar archive but got empty body")
				}
				// Simple validation: tar files should start with a filename and end with null padding
				if len(gotBody) < 512 {
					t.Errorf("tar archive too small: got %d bytes, expected at least 512", len(gotBody))
				}
			} else if tt.want.Body != nil {
				if diff := cmp.Diff(string(wantBody), string(gotBody)); diff != "" {
					t.Errorf("handler returned wrong body (-want +got):\n%s", diff)
				}
			}
			// Verify call count
			if diff := cmp.Diff(len(tt.client.Calls), tt.client.CallCount()); diff != "" {
				t.Errorf("unexpected number of calls (-want +got):\n%s", diff)
			}
		})
	}
}
