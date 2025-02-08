// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestPoolURL(t *testing.T) {
	testCases := []struct {
		name      string
		component string
		pkg       string
		artifact  string
		expected  string
	}{
		{
			name:      "main, artifact matches source",
			component: "main",
			pkg:       "xz-utils",
			artifact:  "xz-utils_5.4.1-0.2.dsc",
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.4.1-0.2.dsc",
		},
		{
			name:      "main, artifact doesn't matches source",
			component: "main",
			pkg:       "xz-utils",
			artifact:  "liblzma5_5.2.5-2.1~deb11u1_amd64.deb",
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/liblzma5_5.2.5-2.1~deb11u1_amd64.deb",
		},
		{
			name:      "package starting with 'lib'",
			component: "main",
			pkg:       "libzip",
			artifact:  "libzip_1.5.1-4.dsc",
			expected:  "https://deb.debian.org/debian/pool/main/libz/libzip/libzip_1.5.1-4.dsc",
		},
		{
			name:      "contrib package",
			component: "contrib",
			pkg:       "alsa-tools",
			artifact:  "alsa-firmware-loaders_1.2.11-1.1_amd64.deb",
			expected:  "https://deb.debian.org/debian/pool/contrib/a/alsa-tools/alsa-firmware-loaders_1.2.11-1.1_amd64.deb",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := PoolURL(tc.component, tc.pkg, tc.artifact)
			if actual != tc.expected {
				t.Errorf("PoolURL mismatch: got %v, want %v", actual, tc.expected)
			}
		})
	}
}

func TestGuessDSCURL(t *testing.T) {
	testCases := []struct {
		name      string
		component string
		pkg       string
		version   string
		artifact  string
		expected  string
	}{
		{
			name:      "non-native",
			component: "main",
			pkg:       "xz-utils",
			version:   "5.4.1-0.2",
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.4.1-0.2.dsc",
		},
		{
			name:      "non-native binary release",
			component: "main",
			pkg:       "xz-utils",
			version:   "5.6.3-1+b1",
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.6.3-1.dsc",
		},
		{
			name:      "stable release",
			component: "main",
			pkg:       "xz-utils",
			version:   "5.2.4-1+deb10u1",
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1+deb10u1.dsc",
		},
		{
			name:      "native",
			component: "main",
			pkg:       "apt",
			version:   "2.2.4",
			expected:  "https://deb.debian.org/debian/pool/main/a/apt/apt_2.2.4.dsc",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := guessDSCURL(tc.component, tc.pkg, tc.version)
			if actual != tc.expected {
				t.Errorf("DSC url mismatch: got %v, want %v", actual, tc.expected)
			}
		})
	}
}

func TestHTTPRegistry_Artifact(t *testing.T) {
	testCases := []struct {
		name        string
		component   string
		pkg         string
		artifact    string
		call        httpxtest.Call
		expected    string
		expectedErr error
	}{
		{
			name:      "",
			component: "main",
			pkg:       "xz-utils",
			artifact:  "xz-utils_5.4.1-0.2_amd64.deb",
			call: httpxtest.Call{
				URL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.4.1-0.2_amd64.deb",
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte("artifact_contents"))),
				},
			},
			expected:    "artifact_contents",
			expectedErr: nil,
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
			actual, err := HTTPRegistry{Client: mockClient}.Artifact(context.Background(), tc.component, tc.pkg, tc.artifact)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if err != nil {
				return
			}
			actualStr := string(must(io.ReadAll(actual)))
			if tc.expected != actualStr {
				if diff := cmp.Diff(actual, tc.expected); diff != "" {
					t.Errorf("Artifact mismatch: diff\n%v", diff)
				}
			}
			if mockClient.CallCount() != 1 {
				t.Errorf("Expected 1 call, got %d", mockClient.CallCount())
			}
		})
	}
}

func TestHTTPRegistry_DSC(t *testing.T) {
	testCases := []struct {
		name        string
		component   string
		pkg         string
		version     string
		expectedURL string
		contents    string
		expected    *DSC
		expectedErr error
	}{
		{
			name:        "No PGP",
			component:   "main",
			pkg:         "xz-utils",
			version:     "5.2.4-1",
			expectedURL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1.dsc",
			contents: `Hash: SHA256

Format: 3.0 (quilt)
Source: xz-utils
Binary: bin-a, bin-b, xz-utils
Package-List:
 liblzma-dev deb libdevel optional arch=any
 liblzma-doc deb doc optional arch=all
Files:
 003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz
 e475651d39fac8c38ff1460c1d92fc2e 879 xz-utils_5.2.4.orig.tar.xz.asc
 5d018428dac6a83f00c010f49c51836e 135296 xz-utils_5.2.4-1.debian.tar.xz`,
			expected: &DSC{
				Stanzas: []ControlStanza{
					{
						Fields: map[string][]string{
							"Hash": {"SHA256"},
						},
					},
					{
						Fields: map[string][]string{
							"Format": {"3.0 (quilt)"},
							"Source": {"xz-utils"},
							"Binary": {"bin-a, bin-b, xz-utils"},
							"Package-List": {
								"liblzma-dev deb libdevel optional arch=any",
								"liblzma-doc deb doc optional arch=all",
							},
							"Files": {
								"003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz",
								"e475651d39fac8c38ff1460c1d92fc2e 879 xz-utils_5.2.4.orig.tar.xz.asc",
								"5d018428dac6a83f00c010f49c51836e 135296 xz-utils_5.2.4-1.debian.tar.xz",
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			name:        "With PGP",
			component:   "main",
			pkg:         "xz-utils",
			version:     "5.2.4-1",
			expectedURL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1.dsc",
			contents: `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA256

Format: 3.0 (quilt)
Source: xz-utils
Binary: bin-a, bin-b, xz-utils
Package-List:
 liblzma-dev deb libdevel optional arch=any
 liblzma-doc deb doc optional arch=all
Files:
 003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz
 e475651d39fac8c38ff1460c1d92fc2e 879 xz-utils_5.2.4.orig.tar.xz.asc
 5d018428dac6a83f00c010f49c51836e 135296 xz-utils_5.2.4-1.debian.tar.xz

-----BEGIN PGP SIGNATURE-----

iQJHBAEBCAAxFiEEUh5Y8X6W1xKqD/EC38Zx7rMz+iUFAlxOW5QTHGpybmllZGVy
RLpmHHG1JOVdOA==
=WDR2
-----END PGP SIGNATURE-----`,
			expected: &DSC{
				Stanzas: []ControlStanza{
					{
						Fields: map[string][]string{
							"Hash": {"SHA256"},
						},
					},
					{
						Fields: map[string][]string{
							"Format": {"3.0 (quilt)"},
							"Source": {"xz-utils"},
							"Binary": {"bin-a, bin-b, xz-utils"},
							"Package-List": {
								"liblzma-dev deb libdevel optional arch=any",
								"liblzma-doc deb doc optional arch=all",
							},
							"Files": {
								"003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz",
								"e475651d39fac8c38ff1460c1d92fc2e 879 xz-utils_5.2.4.orig.tar.xz.asc",
								"5d018428dac6a83f00c010f49c51836e 135296 xz-utils_5.2.4-1.debian.tar.xz",
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			call := httpxtest.Call{
				URL: tc.expectedURL,
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewReader([]byte(tc.contents))),
				},
			}
			mockClient := &httpxtest.MockClient{
				Calls: []httpxtest.Call{call},
				URLValidator: func(expected, actual string) {
					if diff := cmp.Diff(expected, actual); diff != "" {
						t.Fatalf("URL mismatch (-want +got):\n%s", diff)
					}
				},
			}
			DSCURI, actual, err := HTTPRegistry{Client: mockClient}.DSC(context.Background(), tc.component, tc.pkg, tc.version)
			if err != nil && tc.expectedErr != nil && err.Error() != tc.expectedErr.Error() {
				t.Errorf("Error mismatch: got %v, want %v", err, tc.expectedErr)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(actual, tc.expected); diff != "" {
				t.Errorf("DSC mismatch: diff\n%v", diff)
			}
			if diff := cmp.Diff(DSCURI, tc.expectedURL); diff != "" {
				t.Errorf("Returned DSC url doesn't match fetched url\n%v", diff)
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
