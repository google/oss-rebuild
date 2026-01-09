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
	"github.com/google/oss-rebuild/pkg/registry/debian/control"
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
			expected:  "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.6.3-1+b1.dsc",
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
			v, err := ParseVersion(tc.version)
			if err != nil {
				t.Errorf("Unexpected error parsing version: %v", err)
				return
			}
			actual := guessDSCURL(tc.component, tc.pkg, v)
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
		calls       []httpxtest.Call
		expected    string
		expectedErr error
	}{
		{
			name:      "",
			component: "main",
			pkg:       "xz-utils",
			artifact:  "xz-utils_5.4.1-0.2_amd64.deb",
			calls: []httpxtest.Call{
				{
					URL: "https://snapshot.debian.org/mr/package/xz-utils/5.4.1-0.2/binfiles/xz-utils/5.4.1-0.2?fileinfo=1",
					Response: &http.Response{
						StatusCode: 200,
						Body:       httpxtest.Body(`{"fileinfo":{"deadbeef":[{"archive_name":"debian","name":"xz-utils_5.4.1-0.2_amd64.deb"}]}","result":[{"architecture":"amd64","hash":"deadbeef"}]`),
					},
				},
				{
					URL: "https://snapshot.debian.org/file/deadbeef",
					Response: &http.Response{
						StatusCode: 200,
						Body:       httpxtest.Body("artifact_contents"),
					},
				},
			},
			expected:    "artifact_contents",
			expectedErr: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls:        tc.calls,
				URLValidator: httpxtest.NewURLValidator(t),
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
		expected    *control.ControlFile
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
			expected: &control.ControlFile{
				Stanzas: []control.ControlStanza{
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
				Calls:        []httpxtest.Call{call},
				URLValidator: httpxtest.NewURLValidator(t),
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

func TestParseDebianArtifact(t *testing.T) {
	testCases := []struct {
		name                    string
		artifact                string
		expected                ArtifactIdentifier
		expectedRollbackBase    string
		expectedBinaryNMU       string
		expectedNonBinaryString string
		expectedNative          bool
		wantErr                 bool
	}{
		{
			name:     "simple",
			artifact: "apt_2.9.16_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "apt",
				Version: &Version{
					Upstream: "2.9.16",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "",
			expectedNonBinaryString: "2.9.16",
			expectedNative:          true,
			wantErr:                 false,
		},
		{
			name:     "binary version",
			artifact: "libacl1_2.3.2-2+b1_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "libacl1",
				Version: &Version{
					Upstream:       "2.3.2",
					DebianRevision: "2+b1",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "1",
			expectedNonBinaryString: "2.3.2-2",
			expectedNative:          false,
			wantErr:                 false,
		},
		{
			name:     "+deb version",
			artifact: "libfreetype6_2.9.1-3+deb10u3_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "libfreetype6",
				Version: &Version{
					Upstream:       "2.9.1",
					DebianRevision: "3+deb10u3",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "",
			expectedNonBinaryString: "2.9.1-3+deb10u3",
			expectedNative:          false,
			wantErr:                 false,
		},
		{
			name:     "invalid",
			artifact: "a_b.deb", // Not enough components.
			wantErr:  true,
		},
		{
			name:     "~deb version",
			artifact: "akonadi-server_18.08.3-7~deb10u1_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "akonadi-server",
				Version: &Version{
					Upstream:       "18.08.3",
					DebianRevision: "7~deb10u1",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "",
			expectedNonBinaryString: "18.08.3-7~deb10u1",
			expectedNative:          false,
			wantErr:                 false,
		},
		{
			name:     "dash in name",
			artifact: "libadios-bin_1.13.1-16_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "libadios-bin",
				Version: &Version{
					Upstream:       "1.13.1",
					DebianRevision: "16",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "",
			expectedNonBinaryString: "1.13.1-16",
			expectedNative:          false,
			wantErr:                 false,
		},
		{
			name:     "native package binary version",
			artifact: "cdebootstrap-static_0.7.7+b12_amd64.deb",
			expected: ArtifactIdentifier{
				Name: "cdebootstrap-static",
				Version: &Version{
					Upstream:       "0.7.7+b12",
					DebianRevision: "",
				},
				Arch: "amd64",
			},
			expectedRollbackBase:    "",
			expectedBinaryNMU:       "12",
			expectedNonBinaryString: "0.7.7",
			expectedNative:          true,
			wantErr:                 false,
		},
		{
			name:     "rollback with +really",
			artifact: "python3-bibtexparser_2.0.0b5+really1.4.3-1_all.deb",
			expected: ArtifactIdentifier{
				Name: "python3-bibtexparser",
				Version: &Version{
					Upstream:       "2.0.0b5+really1.4.3",
					DebianRevision: "1",
				},
				Arch: "all",
			},
			expectedRollbackBase:    "1.4.3",
			expectedBinaryNMU:       "",
			expectedNonBinaryString: "2.0.0b5+really1.4.3-1",
			expectedNative:          false,
			wantErr:                 false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := ParseDebianArtifact(tc.artifact)
			if (err != nil) != tc.wantErr {
				t.Errorf("WantError mismatch: got %v, want %v", err, tc.wantErr)
				return
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(actual, tc.expected); diff != "" {
				t.Errorf("Artifact mismatch: diff\n%v", diff)
			}
			if source := actual.Version.RollbackBase(); source != tc.expectedRollbackBase {
				t.Errorf("RealUpstream mismatch: got %v, want %v", source, tc.expectedRollbackBase)
			}
			if binaryNMU := actual.Version.BinaryNonMaintainerUpload(); binaryNMU != tc.expectedBinaryNMU {
				t.Errorf("BinaryNMU mismatch: got %v, want %v", binaryNMU, tc.expectedBinaryNMU)
			}
			if nbs := actual.Version.BinaryIndependentString(); nbs != tc.expectedNonBinaryString {
				t.Errorf("NonBinaryString mismatch: got %v, want %v", nbs, tc.expectedNonBinaryString)
			}
			if native := actual.Version.Native(); native != tc.expectedNative {
				t.Errorf("Native mismatch: got %v, want %v", native, tc.expectedNative)
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
