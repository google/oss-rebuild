// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"testing"
)

func TestParseTag(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantLibc  LibcImpl
		wantArch  string
		wantMajor int
		wantMinor int
		wantErr   bool
	}{
		{
			name:      "manylinux1",
			raw:       "manylinux1_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 5,
		},
		{
			name:      "manylinux2010",
			raw:       "manylinux2010_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 12,
		},
		{
			name:      "manylinux2014",
			raw:       "manylinux2014_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 17,
		},
		{
			name:      "pep600_2_17",
			raw:       "manylinux_2_17_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 17,
		},
		{
			name:      "pep600_2_28",
			raw:       "manylinux_2_28_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 28,
		},
		{
			name:      "pep600_2_34",
			raw:       "manylinux_2_34_x86_64",
			wantLibc:  Glibc,
			wantArch:  "x86_64",
			wantMajor: 2,
			wantMinor: 34,
		},
		{
			name:      "pep656_musllinux_1_1",
			raw:       "musllinux_1_1_x86_64",
			wantLibc:  Musl,
			wantArch:  "x86_64",
			wantMajor: 1,
			wantMinor: 1,
		},
		{
			name:      "pep656_musllinux_1_2",
			raw:       "musllinux_1_2_x86_64",
			wantLibc:  Musl,
			wantArch:  "x86_64",
			wantMajor: 1,
			wantMinor: 2,
		},
		{
			name:    "invalid_tag",
			raw:     "linux_x86_64",
			wantErr: true,
		},
		{
			name:    "empty_tag",
			raw:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTag(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTag(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if !tt.wantErr {
				if got.LibcImpl != tt.wantLibc {
					t.Errorf("got Libc = %v, want %v", got.LibcImpl, tt.wantLibc)
				}
				if got.Arch != tt.wantArch {
					t.Errorf("got Arch = %s, want %s", got.Arch, tt.wantArch)
				}
				if got.LibcVersion.Major != tt.wantMajor || got.LibcVersion.Minor != tt.wantMinor {
					t.Errorf("got Version = %v, want %d.%d", got.LibcVersion, tt.wantMajor, tt.wantMinor)
				}
			}
		})
	}
}

func TestParseCompressedTags(t *testing.T) {
	raw := "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64"
	tags, err := ParsePlatformTags(raw)
	if err != nil {
		t.Fatalf("ParseCompressedTags(%q) failed: %v", raw, err)
	}
	if len(tags) != 3 {
		t.Fatalf("got %d tags, want 3", len(tags))
	}
	if tags[0].LibcVersion.Minor != 5 || tags[1].LibcVersion.Minor != 28 || tags[2].LibcVersion.Minor != 5 {
		t.Errorf("unexpected parsed glibc versions: %v", tags)
	}
}

func TestLowestBaselineTag(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantRaw   string
		wantMinor int
	}{
		{
			name:      "compressed set with manylinux1",
			raw:       "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64",
			wantRaw:   "manylinux1_x86_64",
			wantMinor: 5,
		},
		{
			name:      "manylinux2014 and 2_17",
			raw:       "manylinux2014_x86_64.manylinux_2_17_x86_64",
			wantRaw:   "manylinux2014_x86_64",
			wantMinor: 17,
		},
		{
			name:      "single tag",
			raw:       "manylinux_2_28_x86_64",
			wantRaw:   "manylinux_2_28_x86_64",
			wantMinor: 28,
		},
		{
			name:      "musllinux compressed set",
			raw:       "musllinux_1_2_x86_64.musllinux_1_1_x86_64",
			wantRaw:   "musllinux_1_1_x86_64",
			wantMinor: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags, err := ParsePlatformTags(tt.raw)
			if err != nil {
				t.Fatalf("ParseCompressedTags(%q) failed: %v", tt.raw, err)
			}
			lowest, err := LowestLibcVersionTag(tags)
			if err != nil {
				t.Fatalf("LowestBaselineTag failed: %v", err)
			}
			if lowest.LibcVersion.Minor != tt.wantMinor {
				t.Errorf("got lowest version minor = %d, want %d", lowest.LibcVersion.Minor, tt.wantMinor)
			}
		})
	}
}

func TestSelectBaseImage(t *testing.T) {
	tests := []struct {
		name        string
		platformTag string
		wantImage   string
		wantErr     bool
	}{
		{
			name:        "manylinux1 returns 2014 image",
			platformTag: "manylinux1_x86_64",
			wantImage:   ImageManylinux2014X86_64,
		},
		{
			name:        "manylinux2014 returns 2014 image",
			platformTag: "manylinux2014_x86_64",
			wantImage:   ImageManylinux2014X86_64,
		},
		{
			name:        "compressed set with legacy and 2_28 picks lowest baseline 2014 image",
			platformTag: "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64",
			wantImage:   ImageManylinux2014X86_64,
		},
		{
			name:        "manylinux_2_24 selects lower 2014 image",
			platformTag: "manylinux_2_24_x86_64",
			wantImage:   ImageManylinux2014X86_64,
		},
		{
			name:        "manylinux_2_28 returns 2_28 image",
			platformTag: "manylinux_2_28_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "manylinux_2_31 selects lower 2_28 image",
			platformTag: "manylinux_2_31_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "manylinux_2_34 returns 2_34 image",
			platformTag: "manylinux_2_34_x86_64",
			wantImage:   ImageManylinux2_34X86_64,
		},
		{
			name:        "manylinux_2_35 selects lower 2_34 image",
			platformTag: "manylinux_2_35_x86_64",
			wantImage:   ImageManylinux2_34X86_64,
		},
		{
			name:        "musllinux_1_1 returns 1_1 image",
			platformTag: "musllinux_1_1_x86_64",
			wantImage:   ImageMusllinux1_1X86_64,
		},
		{
			name:        "musllinux_1_2 returns 1_2 image",
			platformTag: "musllinux_1_2_x86_64",
			wantImage:   ImageMusllinux1_2X86_64,
		},
		{
			name:        "musllinux compressed set picks lowest 1_1 image",
			platformTag: "musllinux_1_2_x86_64.musllinux_1_1_x86_64",
			wantImage:   ImageMusllinux1_1X86_64,
		},
		{
			name:        "manylinux_2_4 below minimum returns error",
			platformTag: "manylinux_2_4_x86_64",
			wantErr:     true,
		},
		{
			name:        "musllinux_1_0 below minimum returns error",
			platformTag: "musllinux_1_0_x86_64",
			wantErr:     true,
		},
		{
			name:        "empty tag returns error",
			platformTag: "",
			wantErr:     true,
		},
		{
			name:        "non-x86_64 manylinux architecture returns error",
			platformTag: "manylinux2014_aarch64",
			wantErr:     true,
		},
		{
			name:        "non-x86_64 musllinux architecture returns error",
			platformTag: "musllinux_1_1_aarch64",
			wantErr:     true,
		},
		{
			name:        "invalid tag returns error",
			platformTag: "unknown",
			wantErr:     true,
		},
		{
			name:        "unsupported os tag returns error",
			platformTag: "unsupported_os_1_0_x86_64",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectBaseImage(tt.platformTag)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SelectBaseImage(%q) error = %v, wantErr %v", tt.platformTag, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantImage {
				t.Errorf("SelectBaseImage(%q) = %q, want %q", tt.platformTag, got, tt.wantImage)
			}
		})
	}
}
