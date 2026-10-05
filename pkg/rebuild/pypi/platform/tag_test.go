// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
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

func TestHighestLibcVersionTag(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantRaw   string
		wantMinor int
	}{
		{
			name:      "compressed set with manylinux1 and 2_28",
			raw:       "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64",
			wantRaw:   "manylinux_2_28_x86_64",
			wantMinor: 28,
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
			raw:       "musllinux_1_1_x86_64.musllinux_1_2_x86_64",
			wantRaw:   "musllinux_1_2_x86_64",
			wantMinor: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags, err := ParsePlatformTags(tt.raw)
			if err != nil {
				t.Fatalf("ParsePlatformTags(%q) failed: %v", tt.raw, err)
			}
			highest, err := HighestLibcVersionTag(tags)
			if err != nil {
				t.Fatalf("HighestLibcVersionTag failed: %v", err)
			}
			if highest.LibcVersion.Minor != tt.wantMinor {
				t.Errorf("got highest version minor = %d, want %d", highest.LibcVersion.Minor, tt.wantMinor)
			}
			if gotStr := HighestLibcTagString(tt.raw); gotStr != tt.wantRaw {
				t.Errorf("HighestLibcTagString(%q) = %q, want %q", tt.raw, gotStr, tt.wantRaw)
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
			name:        "compressed set with legacy and 2_28 picks highest 2_28 image",
			platformTag: "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "compressed set with 2014 and 2_28 picks highest 2_28 image",
			platformTag: "manylinux2014_x86_64.manylinux_2_17_x86_64.manylinux_2_28_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "manylinux_2_24 rounds up to 2_28 image",
			platformTag: "manylinux_2_24_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "manylinux_2_28 returns 2_28 image",
			platformTag: "manylinux_2_28_x86_64",
			wantImage:   ImageManylinux2_28X86_64,
		},
		{
			name:        "manylinux_2_31 rounds up to 2_34 image",
			platformTag: "manylinux_2_31_x86_64",
			wantImage:   ImageManylinux2_34X86_64,
		},
		{
			name:        "manylinux_2_34 returns 2_34 image",
			platformTag: "manylinux_2_34_x86_64",
			wantImage:   ImageManylinux2_34X86_64,
		},
		{
			name:        "manylinux_2_35 selects 2_34 image",
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
			name:        "musllinux compressed set picks highest 1_2 image",
			platformTag: "musllinux_1_1_x86_64.musllinux_1_2_x86_64",
			wantImage:   ImageMusllinux1_2X86_64,
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

func TestDetectBaseImage(t *testing.T) {
	tests := []struct {
		name    string
		entries []archive.ZipEntry
		want    string
	}{
		{
			name: "CentOS 7 manylinux2014 crti and devtoolset-10",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-311-x86_64-linux-gnu.so"},
				Body: syntheticELFWithComment(elf.EM_X86_64,
					"GCC: (GNU) 4.8.5 20150623 (Red Hat 4.8.5-44)",
					"GCC: (GNU) 10.2.1 20210130 (Red Hat 10.2.1-11)",
				),
			}},
			want: ImageManylinux2014X86_64,
		},
		{
			name: "AlmaLinux 8 manylinux_2_28 crti and gcc-toolset-14",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-312-x86_64-linux-gnu.so"},
				Body: syntheticELFWithComment(elf.EM_X86_64,
					"GCC: (GNU) 14.2.1 20250110 (Red Hat 14.2.1-7)",
					"GCC: (GNU) 8.5.0 20210514 (Red Hat 8.5.0-22)",
				),
			}},
			want: ImageManylinux2_28X86_64,
		},
		{
			name: "AlmaLinux 8 with gcc-toolset-11 distinguishes from AlmaLinux 9",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.abi3.so"},
				Body: syntheticELFWithComment(elf.EM_X86_64,
					"GCC: (GNU) 11.2.1 20220127 (Red Hat 11.2.1-9)",
					"GCC: (GNU) 8.5.0 20210514 (Red Hat 8.5.0-10)",
				),
			}},
			want: ImageManylinux2_28X86_64,
		},
		{
			name: "AlmaLinux 9 manylinux_2_34 crti and gcc-toolset-14",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-312-x86_64-linux-gnu.so"},
				Body: syntheticELFWithComment(elf.EM_X86_64,
					"GCC: (GNU) 11.4.1 20231218 (Red Hat 11.4.1-3)",
					"GCC: (GNU) 14.2.1 20250110 (Red Hat 14.2.1-7)",
				),
			}},
			want: ImageManylinux2_34X86_64,
		},
		{
			name: "Alpine 3.12 musllinux_1_1",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-311-x86_64-linux-musl.so"},
				Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (Alpine 9.3.0) 9.3.0"),
			}},
			want: ImageMusllinux1_1X86_64,
		},
		{
			name: "Alpine 3.19 musllinux_1_1 with GCC 13.2.1_git2023",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-311-x86_64-linux-musl.so"},
				Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (Alpine 13.2.1_git20231014) 13.2.1 20231014"),
			}},
			want: ImageMusllinux1_1X86_64,
		},
		{
			name: "Alpine 3.20 musllinux_1_2 with GCC 13.2.1_git2024",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-312-x86_64-linux-musl.so"},
				Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (Alpine 13.2.1_git20240309) 13.2.1 20240309"),
			}},
			want: ImageMusllinux1_2X86_64,
		},
		{
			name: "Alpine musllinux_1_2 with GCC 14",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-312-x86_64-linux-musl.so"},
				Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (Alpine 14.2.0) 14.2.0"),
			}},
			want: ImageMusllinux1_2X86_64,
		},
		{
			name: "Extension module preferred over bundled .libs library",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg.libs/libfoo-1234.so.1"},
					Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (GNU) 4.8.5 20150623 (Red Hat 4.8.5-44)"),
				},
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-312-x86_64-linux-gnu.so"},
					Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (GNU) 8.5.0 20210514 (Red Hat 8.5.0-22)"),
				},
			},
			want: ImageManylinux2_28X86_64,
		},
		{
			name: "Unrecognized Debian comment returns empty",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-311-x86_64-linux-gnu.so"},
				Body:       syntheticELFWithComment(elf.EM_X86_64, "GCC: (Debian 10.2.1-6) 10.2.1 20210110"),
			}},
			want: "",
		},
		{
			name: "Non-x86_64 ELF returns empty",
			entries: []archive.ZipEntry{{
				FileHeader: &zip.FileHeader{Name: "pkg/_ext.cpython-311-aarch64-linux-gnu.so"},
				Body:       syntheticELFWithComment(elf.EM_AARCH64, "GCC: (GNU) 8.5.0 20210514 (Red Hat 8.5.0-22)"),
			}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, err := archivetest.ZipFile(tt.entries)
			if err != nil {
				t.Fatalf("ZipFile(): %v", err)
			}
			zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatalf("NewReader(): %v", err)
			}
			if got := DetectBaseImage(zr); got != tt.want {
				t.Errorf("DetectBaseImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func syntheticELFWithComment(machine elf.Machine, comments ...string) []byte {
	shstrtab := []byte("\x00.shstrtab\x00.comment\x00")
	commentData := []byte(strings.Join(comments, "\x00") + "\x00")
	const (
		ehdrSize = 64
		shdrSize = 64
		shnum    = 3
		dataOff  = ehdrSize + shnum*shdrSize
	)
	out := make([]byte, dataOff+len(shstrtab)+len(commentData))
	copy(out[0:4], "\x7fELF")
	out[4] = byte(elf.ELFCLASS64)
	out[5] = byte(elf.ELFDATA2LSB)
	out[6] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(out[16:18], uint16(elf.ET_DYN))
	binary.LittleEndian.PutUint16(out[18:20], uint16(machine))
	binary.LittleEndian.PutUint32(out[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint64(out[40:48], ehdrSize)
	binary.LittleEndian.PutUint16(out[52:54], ehdrSize)
	binary.LittleEndian.PutUint16(out[58:60], shdrSize)
	binary.LittleEndian.PutUint16(out[60:62], shnum)
	binary.LittleEndian.PutUint16(out[62:64], 1)
	sh1 := out[ehdrSize+shdrSize : ehdrSize+2*shdrSize]
	binary.LittleEndian.PutUint32(sh1[0:4], 1)
	binary.LittleEndian.PutUint32(sh1[4:8], uint32(elf.SHT_STRTAB))
	binary.LittleEndian.PutUint64(sh1[24:32], dataOff)
	binary.LittleEndian.PutUint64(sh1[32:40], uint64(len(shstrtab)))
	sh2 := out[ehdrSize+2*shdrSize : ehdrSize+3*shdrSize]
	binary.LittleEndian.PutUint32(sh2[0:4], 11)
	binary.LittleEndian.PutUint32(sh2[4:8], uint32(elf.SHT_PROGBITS))
	binary.LittleEndian.PutUint64(sh2[24:32], uint64(dataOff+len(shstrtab)))
	binary.LittleEndian.PutUint64(sh2[32:40], uint64(len(commentData)))
	copy(out[dataOff:], shstrtab)
	copy(out[dataOff+len(shstrtab):], commentData)
	return out
}
