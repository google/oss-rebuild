// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
)

func createTestELF(importedLibs []string, soname string) []byte {
	strtab := []byte{0}
	strOffsets := make(map[string]uint64)
	addStr := func(s string) uint64 {
		if s == "" {
			return 0
		}
		if off, ok := strOffsets[s]; ok {
			return off
		}
		off := uint64(len(strtab))
		strtab = append(strtab, []byte(s)...)
		strtab = append(strtab, 0)
		strOffsets[s] = off
		return off
	}
	for _, lib := range importedLibs {
		addStr(lib)
	}
	sonameOff := addStr(soname)
	type dynEntry struct {
		tag uint64
		val uint64
	}
	var dynEntries []dynEntry
	for _, lib := range importedLibs {
		dynEntries = append(dynEntries, dynEntry{tag: 1, val: strOffsets[lib]}) // DT_NEEDED
	}
	if soname != "" {
		dynEntries = append(dynEntries, dynEntry{tag: 14, val: sonameOff}) // DT_SONAME
	}
	dynEntries = append(dynEntries, dynEntry{tag: 5, val: 0})                    // DT_STRTAB
	dynEntries = append(dynEntries, dynEntry{tag: 10, val: uint64(len(strtab))}) // DT_STRSZ
	dynEntries = append(dynEntries, dynEntry{tag: 0, val: 0})                    // DT_NULL
	shstrtab := []byte("\x00.dynamic\x00.dynstr\x00.shstrtab\x00")
	dynDataOffset := uint64(64 + 4*64)
	dynDataSize := uint64(len(dynEntries) * 16)
	dynstrDataOffset := dynDataOffset + dynDataSize
	dynstrDataSize := uint64(len(strtab))
	shstrtabDataOffset := dynstrDataOffset + dynstrDataSize
	shstrtabDataSize := uint64(len(shstrtab))
	buf := new(bytes.Buffer)
	// ELF64 Header (64 bytes)
	hdr := make([]byte, 64)
	copy(hdr[0:4], []byte{0x7f, 'E', 'L', 'F'})
	hdr[4] = 2                                    // ELFCLASS64
	hdr[5] = 1                                    // ELFDATA2LSB
	hdr[6] = 1                                    // EV_CURRENT
	binary.LittleEndian.PutUint16(hdr[16:18], 3)  // ET_DYN
	binary.LittleEndian.PutUint16(hdr[18:20], 62) // EM_X86_64
	binary.LittleEndian.PutUint32(hdr[20:24], 1)  // e_version
	binary.LittleEndian.PutUint64(hdr[40:48], 64) // e_shoff
	binary.LittleEndian.PutUint16(hdr[52:54], 64) // e_ehsize
	binary.LittleEndian.PutUint16(hdr[58:60], 64) // e_shentsize
	binary.LittleEndian.PutUint16(hdr[60:62], 4)  // e_shnum
	binary.LittleEndian.PutUint16(hdr[62:64], 3)  // e_shstrndx
	buf.Write(hdr)
	// Section 0: SHT_NULL (64 bytes)
	buf.Write(make([]byte, 64))
	// Section 1: .dynamic (64 bytes)
	shDyn := make([]byte, 64)
	binary.LittleEndian.PutUint32(shDyn[0:4], 1)               // sh_name (".dynamic")
	binary.LittleEndian.PutUint32(shDyn[4:8], 6)               // sh_type (SHT_DYNAMIC)
	binary.LittleEndian.PutUint64(shDyn[8:16], 3)              // sh_flags (SHF_WRITE | SHF_ALLOC)
	binary.LittleEndian.PutUint64(shDyn[16:24], 0x1000)        // sh_addr
	binary.LittleEndian.PutUint64(shDyn[24:32], dynDataOffset) // sh_offset
	binary.LittleEndian.PutUint64(shDyn[32:40], dynDataSize)   // sh_size
	binary.LittleEndian.PutUint32(shDyn[40:44], 2)             // sh_link (section 2 is .dynstr)
	binary.LittleEndian.PutUint64(shDyn[48:56], 8)             // sh_addralign
	binary.LittleEndian.PutUint64(shDyn[56:64], 16)            // sh_entsize
	buf.Write(shDyn)
	// Section 2: .dynstr (64 bytes)
	shDynStr := make([]byte, 64)
	binary.LittleEndian.PutUint32(shDynStr[0:4], 10)                 // sh_name (".dynstr")
	binary.LittleEndian.PutUint32(shDynStr[4:8], 3)                  // sh_type (SHT_STRTAB)
	binary.LittleEndian.PutUint64(shDynStr[8:16], 2)                 // sh_flags (SHF_ALLOC)
	binary.LittleEndian.PutUint64(shDynStr[16:24], 0x2000)           // sh_addr
	binary.LittleEndian.PutUint64(shDynStr[24:32], dynstrDataOffset) // sh_offset
	binary.LittleEndian.PutUint64(shDynStr[32:40], dynstrDataSize)   // sh_size
	binary.LittleEndian.PutUint64(shDynStr[48:56], 1)                // sh_addralign
	buf.Write(shDynStr)
	// Section 3: .shstrtab (64 bytes)
	shShStrTab := make([]byte, 64)
	binary.LittleEndian.PutUint32(shShStrTab[0:4], 18)                   // sh_name (".shstrtab")
	binary.LittleEndian.PutUint32(shShStrTab[4:8], 3)                    // sh_type (SHT_STRTAB)
	binary.LittleEndian.PutUint64(shShStrTab[24:32], shstrtabDataOffset) // sh_offset
	binary.LittleEndian.PutUint64(shShStrTab[32:40], shstrtabDataSize)   // sh_size
	binary.LittleEndian.PutUint64(shShStrTab[48:56], 1)                  // sh_addralign
	buf.Write(shShStrTab)
	for _, d := range dynEntries {
		binary.Write(buf, binary.LittleEndian, d.tag)
		binary.Write(buf, binary.LittleEndian, d.val)
	}
	buf.Write(strtab)
	buf.Write(shstrtab)
	return buf.Bytes()
}

func TestIsStandardLibrary(t *testing.T) {
	tests := []struct {
		name   string
		soname string
		want   bool
	}{
		{"Glibc", "libc.so.6", true},
		{"DynamicLinkerX86_64", "ld-linux-x86-64.so.2", true},
		{"MuslLibc", "libc.musl-x86_64.so.1", true},
		{"MuslLinker", "ld-musl-x86_64.so.1", true},
		{"LibPython", "libpython3.11.so.1.0", true},
		{"Stdcpp", "libstdc++.so.6", true},
		{"ExternalCgraph", "libcgraph.so.6", false},
		{"OpenGLAllowedToInstallHeaders", "libGL.so.1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStandardLibrary(tc.soname); got != tc.want {
				t.Errorf("isStandardLibrary(%q) = %v, want %v", tc.soname, got, tc.want)
			}
		})
	}
}

func TestIsBundledLibPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"PackageDotLibsDir", "pygraphviz.libs/libgvc-12345678.so.6.0.0", true},
		{"NestedDotLibsDir", "pkg/.libs/libfoo.so.1", true},
		{"LegacyDotLibsUnderscoreDir", ".libs_cffi_backend/libffi-12345678.so.8.1.0", true},
		{"PackageRootExtension", "pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so", false},
		{"PackageInternalLibDir", "torch/lib/libc10.so", false},
		{"UnrelatedPrefix", "foo/.libsofar/libbar.so.1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBundledLibPath(tc.path); got != tc.want {
				t.Errorf("isBundledLibPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestLibraryStem(t *testing.T) {
	tests := []struct {
		name   string
		soname string
		want   string
	}{
		{"VersionedSoname", "libcgraph.so.6", "cgraph"},
		{"MultiPartVersion", "libcdt.so.5.0.0", "cdt"},
		{"UnversionedSoname", "libpq.so", "pq"},
		{"SingleCharStem", "libz.so.1", "z"},
		{"StemContainingDotSo", "libfoo.some.so.1", "foo.some"},
		{"MissingLibPrefix", "graphviz.so", ""},
		{"EmptyStem", "lib.so", ""},
		{"CPythonExtensionModule", "libfoo.cpython-311-x86_64-linux-gnu.so", ""},
		{"PyPyExtensionModule", "libfoo.pypy39-pp73-x86_64-linux-gnu.so", ""},
		{"ABI3ExtensionModule", "libfoo.abi3.so", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := libraryStem(tc.soname); got != tc.want {
				t.Errorf("libraryStem(%q) = %q, want %q", tc.soname, got, tc.want)
			}
		})
	}
}

func TestUnbundledSoname(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"PlainSoname", "libcgraph.so.6", "libcgraph.so.6"},
		{"AuditwheelHashedMajor", "libcgraph-17278668.so.6", "libcgraph.so.6"},
		{"AuditwheelHashedFullVersion", "libcgraph-17278668.so.6.0.0", "libcgraph.so.6"},
		{"TwoPartVersionPreserved", "libssl-abcdef12.so.1.1", "libssl.so.1.1"},
		{"AuditwheelHashBeforeFirstDot", "libopenblas64_p-r0-0cf96a72.3.23.dev.so", "libopenblas64_p-r0.3.23.dev.so"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unbundledSoname(tc.input); got != tc.want {
				t.Errorf("unbundledSoname(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestInferELFSystemDeps(t *testing.T) {
	extELF := createTestELF([]string{
		"libc.so.6",
		"libcgraph-17278668.so.6.0.0",
		"libcdt.so.5",
		"libpython3.10.so.1.0",
		"_helper.cpython-310-x86_64-linux-gnu.so",
		"libc10.so",
	}, "")
	bundledELF := createTestELF([]string{"libc.so.6", "libexpat.so.1"}, "libgvc.so.6")
	internalWheelELF := createTestELF([]string{"libstdc++.so.6"}, "libc10.so")
	entries := []archive.ZipEntry{
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so"},
			Body:       extELF,
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz.libs/libgvc-12345678.so.6.2"},
			Body:       bundledELF,
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz.libs/libpathplan-abcdef01.so.4.0.0"},
			Body:       []byte("non-elf bundled file"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz.libs/libignored.so.debug"},
			Body:       []byte("debug info"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz/lib/libc10.so"},
			Body:       internalWheelELF,
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz/graph.py"},
			Body:       []byte("print('hello')"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "pygraphviz/corrupt.so"},
			Body:       []byte("not an elf binary"),
		},
	}
	buf := must(archivetest.ZipFile(entries))
	zr := must(zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())))
	tests := []struct {
		name string
		libc platform.LibcImpl
		want []string
	}{
		{
			name: "GlibcManylinux",
			libc: platform.Glibc,
			want: []string{
				"libcgraph.so.6()(64bit)",
				"/usr/lib64/libcgraph.so",
				"libcdt.so.5()(64bit)",
				"/usr/lib64/libcdt.so",
				"libgvc.so.6()(64bit)",
				"/usr/lib64/libgvc.so",
				"libexpat.so.1()(64bit)",
				"/usr/lib64/libexpat.so",
				"libpathplan.so.4()(64bit)",
				"/usr/lib64/libpathplan.so",
			},
		},
		{
			name: "MuslMusllinux",
			libc: platform.Musl,
			want: []string{
				"so:libcgraph.so.6",
				"pc:libcgraph",
				"cgraph-dev",
				"so:libcdt.so.5",
				"pc:libcdt",
				"cdt-dev",
				"so:libgvc.so.6",
				"pc:libgvc",
				"gvc-dev",
				"so:libexpat.so.1",
				"pc:libexpat",
				"expat-dev",
				"so:libpathplan.so.4",
				"pc:libpathplan",
				"pathplan-dev",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := inferELFSystemDeps(zr, tc.libc)
			if err != nil {
				t.Fatalf("inferELFSystemDeps() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("inferELFSystemDeps() diff (-want +got):\n%s", diff)
			}
		})
	}
}
