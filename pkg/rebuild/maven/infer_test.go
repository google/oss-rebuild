// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"
)

func Test_getClassFileMajorVersion(t *testing.T) {
	testCases := []struct {
		name       string
		classBytes []byte
		want       int
		wantErr    bool
	}{
		{
			name: "Valid Java 8 class file",
			// Magic, minor, major version 52 (0x34)
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34, 0x01, 0x02},
			want:       8,
			wantErr:    false,
		},
		{
			name: "Valid Java 11 class file",
			// Magic, minor, major version 55 (0x37)
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x37, 0x01, 0x02},
			want:       11,
			wantErr:    false,
		},
		{
			name:       "File too short",
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE},
			wantErr:    true,
		},
		{
			name:       "Invalid magic number",
			classBytes: []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x34},
			wantErr:    true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getClassFileMajorVersion(tc.classBytes)
			if (err != nil) != tc.wantErr {
				t.Errorf("getClassFileMajorVersion() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("getClassFileMajorVersion() = %v, want %v", got, tc.want)
			}
		})
	}
}
