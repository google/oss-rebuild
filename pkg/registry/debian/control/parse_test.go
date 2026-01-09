// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		contents    string
		expectedErr bool
		expected    *ControlFile
	}{
		{
			name: "DSC No PGP",
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
			expectedErr: false,
			expected: &ControlFile{
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
		},
		{
			name: "With PGP",
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
			expectedErr: false,
			expected: &ControlFile{
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tt.contents))
			if (err != nil) != tt.expectedErr {
				t.Errorf("Parse() error = %v, expectedErr %v", err, tt.expectedErr)
				return
			}
			if tt.expectedErr {
				return
			}
			if diff := cmp.Diff(got, tt.expected); diff != "" {
				t.Errorf("Control file mismatch: diff\n%v", diff)
			}
		})
	}
}

func TestParseBuildInfo(t *testing.T) {
	tests := []struct {
		name        string
		contents    string
		expectedErr bool
		expected    *BuildInfo
	}{
		{
			name: "BuildInfo",
			contents: `Format: 1.0
Source: xz-utils
Binary: xz-utils
Architecture: amd64
Version: 5.2.4-1
Build-Origin: Debian
Build-Architecture: amd64
Build-Date: Sun, 28 Oct 2018 15:53:24 +0000
Build-Path: /build/xz-utils-5.2.4
Installed-Build-Depends:
 autoconf (= 2.69-11),
 automake (= 1:1.16.1-4),
Environment:
 DEB_BUILD_OPTIONS="parallel=4"
 LANG="C.UTF-8"
Checksums-Sha256:
 003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz`,
			expectedErr: false,
			expected: &BuildInfo{
				Format:            "1.0",
				Source:            "xz-utils",
				Binary:            []string{"xz-utils"},
				Architecture:      "amd64",
				Version:           "5.2.4-1",
				BuildOrigin:       "Debian",
				BuildArchitecture: "amd64",
				BuildDate:         "Sun, 28 Oct 2018 15:53:24 +0000",
				BuildPath:         "/build/xz-utils-5.2.4",
				InstalledBuildDepends: []string{
					"autoconf (= 2.69-11)",
					"automake (= 1:1.16.1-4)",
				},
				Environment: []string{
					"DEB_BUILD_OPTIONS=\"parallel=4\"",
					"LANG=\"C.UTF-8\"",
				},
				ChecksumsSha256: []string{
					"003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBuildInfo(strings.NewReader(tt.contents))
			if (err != nil) != tt.expectedErr {
				t.Errorf("ParseBuildInfo() error = %v, expectedErr %v", err, tt.expectedErr)
				return
			}
			if tt.expectedErr {
				return
			}
			if diff := cmp.Diff(got, tt.expected); diff != "" {
				t.Errorf("BuildInfo mismatch: diff\n%v", diff)
			}
		})
	}
}
