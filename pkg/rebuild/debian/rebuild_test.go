// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	regdeb "github.com/google/oss-rebuild/pkg/registry/debian"
)

func TestRebuilder_Upstream(t *testing.T) {
	testCases := []struct {
		name         string
		target       rebuild.Target
		makeCalls    func() []httpxtest.Call
		expectedURL  string
		expectedBody string
		expectedErr  bool
	}{
		{
			name: "WithEpoch0",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/xz-utils",
				Version:   "0:5.4.1-0.2",
				Artifact:  "xz-utils_5.4.1-0.2_amd64.deb",
			},
			makeCalls: func() []httpxtest.Call {
				return []httpxtest.Call{
					{
						URL: "https://snapshot.debian.org/mr/package/xz-utils/5.4.1-0.2/binfiles/xz-utils/5.4.1-0.2?fileinfo=1",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(`{"fileinfo":{"deadbeef":[{"archive_name":"debian","name":"xz-utils_5.4.1-0.2_amd64.deb"}]},"result":[{"architecture":"amd64","hash":"deadbeef"}]}`),
						},
					},
					{
						URL: "https://snapshot.debian.org/file/deadbeef",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body("xz_deb_content"),
						},
					},
				}
			},
			expectedURL:  "https://snapshot.debian.org/file/deadbeef",
			expectedBody: "xz_deb_content",
		},
		{
			name: "WithEpoch",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/xrt",
				Version:   "1:2.25.0-4",
				Artifact:  "xrt-xocl-dkms_2.25.0-4_amd64.deb",
			},
			makeCalls: func() []httpxtest.Call {
				return []httpxtest.Call{
					{
						URL: "https://snapshot.debian.org/mr/package/xrt/1:2.25.0-4/binfiles/xrt-xocl-dkms/1:2.25.0-4?fileinfo=1",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(`{"fileinfo":{"c1d9b002":[{"archive_name":"debian","name":"xrt-xocl-dkms_2.25.0-4_amd64.deb"}]},"result":[{"architecture":"amd64","hash":"c1d9b002"}]}`),
						},
					},
					{
						URL: "https://snapshot.debian.org/file/c1d9b002",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body("xrt_deb_content"),
						},
					},
				}
			},
			expectedURL:  "https://snapshot.debian.org/file/c1d9b002",
			expectedBody: "xrt_deb_content",
		},
		{
			name: "WithHigherEpoch",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/xwayland",
				Version:   "2:24.1.13-1",
				Artifact:  "xwayland_24.1.13-1_amd64.deb",
			},
			makeCalls: func() []httpxtest.Call {
				return []httpxtest.Call{
					{
						URL: "https://snapshot.debian.org/mr/package/xwayland/2:24.1.13-1/binfiles/xwayland/2:24.1.13-1?fileinfo=1",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(`{"fileinfo":{"6f36b6dc":[{"archive_name":"debian","name":"xwayland_24.1.13-1_amd64.deb"}]},"result":[{"architecture":"amd64","hash":"6f36b6dc"}]}`),
						},
					},
					{
						URL: "https://snapshot.debian.org/file/6f36b6dc",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body("xwayland_deb_content"),
						},
					},
				}
			},
			expectedURL:  "https://snapshot.debian.org/file/6f36b6dc",
			expectedBody: "xwayland_deb_content",
		},
		{
			name: "WithEpochAndBinaryNMU",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/ffmpeg",
				Version:   "7:8.1.2-2+b3",
				Artifact:  "ffmpeg_8.1.2-2+b3_amd64.deb",
			},
			makeCalls: func() []httpxtest.Call {
				return []httpxtest.Call{
					{
						URL: "https://snapshot.debian.org/mr/package/ffmpeg/7:8.1.2-2/binfiles/ffmpeg/7:8.1.2-2+b3?fileinfo=1",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(`{"fileinfo":{"e7661279":[{"archive_name":"debian","name":"ffmpeg_8.1.2-2+b3_amd64.deb"}]},"result":[{"architecture":"amd64","hash":"e7661279"}]}`),
						},
					},
					{
						URL: "https://snapshot.debian.org/file/e7661279",
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body("ffmpeg_deb_content"),
						},
					},
				}
			},
			expectedURL:  "https://snapshot.debian.org/file/e7661279",
			expectedBody: "ffmpeg_deb_content",
		},
		{
			name: "NoComponent",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "invalidpkg",
				Version:   "1.0",
				Artifact:  "pkg_1.0_amd64.deb",
			},
			expectedErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rebuilder := Rebuilder{}

			if tc.expectedErr {
				mux := rebuild.RegistryMux{
					Debian: regdeb.HTTPRegistry{},
				}
				_, err := rebuilder.Upstream(context.Background(), tc.target, mux)
				if err == nil {
					t.Fatalf("expected error from Upstream, got nil")
				}
				_, err = rebuilder.UpstreamURL(context.Background(), tc.target, mux)
				if err == nil {
					t.Fatalf("expected error from UpstreamURL, got nil")
				}
				return
			}

			// Test Upstream
			mockClient := &httpxtest.MockClient{
				Calls:        tc.makeCalls(),
				URLValidator: httpxtest.NewURLValidator(t),
			}
			mux := rebuild.RegistryMux{
				Debian: regdeb.HTTPRegistry{Client: mockClient},
			}
			rc, err := rebuilder.Upstream(context.Background(), tc.target, mux)
			if err != nil {
				t.Fatalf("rebuilder.Upstream failed unexpectedly: %v", err)
			}
			defer rc.Close()
			body, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("io.ReadAll failed: %v", err)
			}
			if diff := cmp.Diff(string(body), tc.expectedBody); diff != "" {
				t.Errorf("Upstream body mismatch (-got +want):\n%s", diff)
			}

			// Test UpstreamURL
			mockClientURL := &httpxtest.MockClient{
				Calls:        tc.makeCalls()[:1], // UpstreamURL only needs the first call
				URLValidator: httpxtest.NewURLValidator(t),
			}
			muxURL := rebuild.RegistryMux{
				Debian: regdeb.HTTPRegistry{Client: mockClientURL},
			}
			u, err := rebuilder.UpstreamURL(context.Background(), tc.target, muxURL)
			if err != nil {
				t.Fatalf("rebuilder.UpstreamURL failed unexpectedly: %v", err)
			}
			if u != tc.expectedURL {
				t.Errorf("UpstreamURL mismatch: got %v, want %v", u, tc.expectedURL)
			}
		})
	}
}
