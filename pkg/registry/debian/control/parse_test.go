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
