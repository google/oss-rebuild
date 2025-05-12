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
			wantBody, err := io.ReadAll(tt.want.Body)
			if err != nil {
				t.Fatalf("Failed to read expected body: %v", err)
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
			} else {
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
