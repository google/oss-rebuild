// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestWantsSimpleHTML(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{
			name:   "EmptyHeader",
			accept: "",
			want:   false,
		},
		{
			name:   "PEP691JSON",
			accept: "application/vnd.pypi.simple.v1+json",
			want:   false,
		},
		{
			name:   "PEP691JSONWithHTMLFallback",
			accept: "application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.2, text/html;q=0.01",
			want:   false,
		},
		{
			name:   "LegacyPipSimpleHTML",
			accept: "application/vnd.pypi.simple.v1+html, text/html;q=0.9",
			want:   true,
		},
		{
			name:   "LegacyPipTextHTML",
			accept: "text/html",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsSimpleHTML(tt.accept); got != tt.want {
				t.Errorf("wantsSimpleHTML(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}

func TestHandlePyPISimpleHTML(t *testing.T) {
	client := &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{
				Method: "GET",
				URL:    "https://pypi.org/simple/some-package/",
				Response: &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type": []string{"application/vnd.pypi.simple.v1+json"},
					},
					Body: io.NopCloser(bytes.NewBufferString(`{
						"name": "some-package",
						"files": [
							{
								"filename": "some-package-0.9.0.tar.gz",
								"url": "https://files.pythonhosted.org/packages/some-package-0.9.0.tar.gz",
								"hashes": {"sha256": "deadbeef"},
								"upload-time": "2021-01-01T00:00:00.123456Z",
								"yanked": "broken release"
							},
							{
								"filename": "some-package-1.0.0-py3-none-any.whl",
								"url": "https://files.pythonhosted.org/packages/some-package-1.0.0-py3-none-any.whl",
								"hashes": {"sha256": "cafebabe"},
								"requires-python": ">=3.6, !=3.0.*",
								"upload-time": "2021-06-02T00:00:00.123456Z",
								"yanked": false
							},
							{
								"filename": "some-package-2.0.0.tar.gz",
								"url": "https://files.pythonhosted.org/packages/some-package-2.0.0.tar.gz",
								"hashes": {"sha256": "feedface"},
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
	}
	handler := &Handler{Client: client}
	req := httptest.NewRequest("GET", "http://localhost:8081/simple/some-package/", nil)
	req.SetBasicAuth("pypi", "2022-01-01T00:00:00Z")
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+html, text/html;q=0.9")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	got := rr.Result()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", got.StatusCode, http.StatusOK)
	}
	if ct := got.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
	gotBody, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	wantBody := `<!DOCTYPE html><html><body><a href="https://files.pythonhosted.org/packages/some-package-0.9.0.tar.gz#sha256=deadbeef" data-yanked="broken release">some-package-0.9.0.tar.gz</a><br/><a href="https://files.pythonhosted.org/packages/some-package-1.0.0-py3-none-any.whl#sha256=cafebabe" data-requires-python="&gt;=3.6, !=3.0.*">some-package-1.0.0-py3-none-any.whl</a><br/></body></html>`
	if diff := cmp.Diff(wantBody, string(gotBody)); diff != "" {
		t.Errorf("response body diff (-want +got):\n%s", diff)
	}
}
