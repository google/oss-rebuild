// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
)

func createTestELF(importedLibs []string, soname string) []byte {
	return createTestELFWithSections(importedLibs, soname, nil)
}

func createTestELFWithSections(importedLibs []string, soname string, extraSections map[string][]byte) []byte {
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
	var extraNames []string
	for name := range extraSections {
		extraNames = append(extraNames, name)
	}
	slices.Sort(extraNames)
	extraNameOffs := make(map[string]uint32, len(extraNames))
	for _, name := range extraNames {
		extraNameOffs[name] = uint32(len(shstrtab))
		shstrtab = append(shstrtab, []byte(name)...)
		shstrtab = append(shstrtab, 0)
	}
	numSections := uint16(4 + len(extraNames))
	dynDataOffset := uint64(64 + uint64(numSections)*64)
	dynDataSize := uint64(len(dynEntries) * 16)
	dynstrDataOffset := dynDataOffset + dynDataSize
	dynstrDataSize := uint64(len(strtab))
	shstrtabDataOffset := dynstrDataOffset + dynstrDataSize
	shstrtabDataSize := uint64(len(shstrtab))
	buf := new(bytes.Buffer)
	// ELF64 Header (64 bytes)
	hdr := make([]byte, 64)
	copy(hdr[0:4], []byte{0x7f, 'E', 'L', 'F'})
	hdr[4] = 2                                             // ELFCLASS64
	hdr[5] = 1                                             // ELFDATA2LSB
	hdr[6] = 1                                             // EV_CURRENT
	binary.LittleEndian.PutUint16(hdr[16:18], 3)           // ET_DYN
	binary.LittleEndian.PutUint16(hdr[18:20], 62)          // EM_X86_64
	binary.LittleEndian.PutUint32(hdr[20:24], 1)           // e_version
	binary.LittleEndian.PutUint64(hdr[40:48], 64)          // e_shoff
	binary.LittleEndian.PutUint16(hdr[52:54], 64)          // e_ehsize
	binary.LittleEndian.PutUint16(hdr[58:60], 64)          // e_shentsize
	binary.LittleEndian.PutUint16(hdr[60:62], numSections) // e_shnum
	binary.LittleEndian.PutUint16(hdr[62:64], 3)           // e_shstrndx
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
	extraOffset := shstrtabDataOffset + shstrtabDataSize
	for _, name := range extraNames {
		secData := extraSections[name]
		shExtra := make([]byte, 64)
		binary.LittleEndian.PutUint32(shExtra[0:4], extraNameOffs[name])
		binary.LittleEndian.PutUint32(shExtra[4:8], 1) // SHT_PROGBITS
		binary.LittleEndian.PutUint64(shExtra[24:32], extraOffset)
		binary.LittleEndian.PutUint64(shExtra[32:40], uint64(len(secData)))
		binary.LittleEndian.PutUint64(shExtra[48:56], 1)
		buf.Write(shExtra)
		extraOffset += uint64(len(secData))
	}
	for _, d := range dynEntries {
		binary.Write(buf, binary.LittleEndian, d.tag)
		binary.Write(buf, binary.LittleEndian, d.val)
	}
	buf.Write(strtab)
	buf.Write(shstrtab)
	for _, name := range extraNames {
		buf.Write(extraSections[name])
	}
	return buf.Bytes()
}

func TestInferELFSystemDeps(t *testing.T) {
	extELF := createTestELF([]string{
		"libc.so.6",
		"libgfortran-12345678.so.5.0.0",
		"libcgraph-17278668.so.6.0.0",
		"libcdt.so.5",
		"libssl-abcdef12.so.1.1",
		"libpython3.10.so.1.0",
		"_helper.cpython-310-x86_64-linux-gnu.so",
		"libfoo.abi3.so",
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
			Body:       createTestELF(nil, ""),
		},
		{
			FileHeader: &zip.FileHeader{Name: ".libs_cffi_backend/libffi-12345678.so.8.1.0"},
			Body:       createTestELF(nil, ""),
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
				"libssl.so.1.1()(64bit)",
				"/usr/lib64/libssl.so",
				"libgvc.so.6()(64bit)",
				"/usr/lib64/libgvc.so",
				"libexpat.so.1()(64bit)",
				"/usr/lib64/libexpat.so",
				"libpathplan.so.4()(64bit)",
				"/usr/lib64/libpathplan.so",
				"libffi.so.8()(64bit)",
				"/usr/lib64/libffi.so",
			},
		},
		{
			name: "MuslMusllinux",
			libc: platform.Musl,
			want: []string{
				"so:libcgraph.so.6",
				"pc:libcgraph",
				"so:libcdt.so.5",
				"pc:libcdt",
				"so:libssl.so.1.1",
				"pc:libssl",
				"so:libgvc.so.6",
				"pc:libgvc",
				"so:libexpat.so.1",
				"pc:libexpat",
				"so:libpathplan.so.4",
				"pc:libpathplan",
				"so:libffi.so.8",
				"pc:libffi",
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
