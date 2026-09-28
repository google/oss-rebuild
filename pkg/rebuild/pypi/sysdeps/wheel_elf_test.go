// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func createSyntheticELF(neededLibs []string) []byte {
	buf := new(bytes.Buffer)
	// Build string table
	strtab := []byte{0}
	strOffsets := make([]uint64, len(neededLibs))
	for i, lib := range neededLibs {
		strOffsets[i] = uint64(len(strtab))
		strtab = append(strtab, []byte(lib)...)
		strtab = append(strtab, 0)
	}

	shstrtab := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	dynstrNameOffset := uint32(1)
	dynamicNameOffset := uint32(9)
	shstrtabNameOffset := uint32(18)

	ehdrSize := uint64(64)
	phdrSize := uint64(56)
	phnum := uint64(2)
	dynEntrySize := uint64(16)
	dynCount := uint64(len(neededLibs) + 3) // DT_NEEDEDs + DT_STRTAB + DT_STRSZ + DT_NULL
	dynSize := dynCount * dynEntrySize
	dynOffset := ehdrSize + phnum*phdrSize
	strtabOffset := dynOffset + dynSize
	shstrtabOffset := strtabOffset + uint64(len(strtab))
	shdrSize := uint64(64)
	shnum := uint64(4)
	shoff := shstrtabOffset + uint64(len(shstrtab))
	totalSize := shoff + shnum*shdrSize

	// Write ELF Header
	ident := [16]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	buf.Write(ident[:])
	binary.Write(buf, binary.LittleEndian, uint16(3))        // e_type: ET_DYN
	binary.Write(buf, binary.LittleEndian, uint16(62))       // e_machine: EM_X86_64
	binary.Write(buf, binary.LittleEndian, uint32(1))        // e_version
	binary.Write(buf, binary.LittleEndian, uint64(0))        // e_entry
	binary.Write(buf, binary.LittleEndian, uint64(ehdrSize)) // e_phoff
	binary.Write(buf, binary.LittleEndian, uint64(shoff))    // e_shoff
	binary.Write(buf, binary.LittleEndian, uint32(0))        // e_flags
	binary.Write(buf, binary.LittleEndian, uint16(ehdrSize)) // e_ehsize
	binary.Write(buf, binary.LittleEndian, uint16(phdrSize)) // e_phentsize
	binary.Write(buf, binary.LittleEndian, uint16(phnum))    // e_phnum
	binary.Write(buf, binary.LittleEndian, uint16(shdrSize)) // e_shentsize
	binary.Write(buf, binary.LittleEndian, uint16(shnum))    // e_shnum
	binary.Write(buf, binary.LittleEndian, uint16(3))        // e_shstrndx

	// Write Program Header 0: PT_LOAD
	binary.Write(buf, binary.LittleEndian, uint32(1))         // p_type: PT_LOAD
	binary.Write(buf, binary.LittleEndian, uint32(7))         // p_flags: PF_R | PF_W | PF_X
	binary.Write(buf, binary.LittleEndian, uint64(0))         // p_offset
	binary.Write(buf, binary.LittleEndian, uint64(0))         // p_vaddr
	binary.Write(buf, binary.LittleEndian, uint64(0))         // p_paddr
	binary.Write(buf, binary.LittleEndian, uint64(totalSize)) // p_filesz
	binary.Write(buf, binary.LittleEndian, uint64(totalSize)) // p_memsz
	binary.Write(buf, binary.LittleEndian, uint64(0x1000))    // p_align

	// Write Program Header 1: PT_DYNAMIC
	binary.Write(buf, binary.LittleEndian, uint32(2))         // p_type: PT_DYNAMIC
	binary.Write(buf, binary.LittleEndian, uint32(6))         // p_flags: PF_R | PF_W
	binary.Write(buf, binary.LittleEndian, uint64(dynOffset)) // p_offset
	binary.Write(buf, binary.LittleEndian, uint64(dynOffset)) // p_vaddr
	binary.Write(buf, binary.LittleEndian, uint64(dynOffset)) // p_paddr
	binary.Write(buf, binary.LittleEndian, uint64(dynSize))   // p_filesz
	binary.Write(buf, binary.LittleEndian, uint64(dynSize))   // p_memsz
	binary.Write(buf, binary.LittleEndian, uint64(8))         // p_align

	// Write Dynamic Entries
	for _, off := range strOffsets {
		binary.Write(buf, binary.LittleEndian, uint64(1))   // d_tag: DT_NEEDED
		binary.Write(buf, binary.LittleEndian, uint64(off)) // d_val: strtab offset
	}
	binary.Write(buf, binary.LittleEndian, uint64(5))            // d_tag: DT_STRTAB
	binary.Write(buf, binary.LittleEndian, uint64(strtabOffset)) // d_val: strtab offset
	binary.Write(buf, binary.LittleEndian, uint64(10))           // d_tag: DT_STRSZ
	binary.Write(buf, binary.LittleEndian, uint64(len(strtab)))  // d_val: strtab size
	binary.Write(buf, binary.LittleEndian, uint64(0))            // d_tag: DT_NULL
	binary.Write(buf, binary.LittleEndian, uint64(0))            // d_val: 0

	// Write String Table
	buf.Write(strtab)
	// Write Section Header String Table
	buf.Write(shstrtab)

	// Section 0: NULL
	buf.Write(make([]byte, 64))

	// Section 1: .dynstr
	binary.Write(buf, binary.LittleEndian, uint32(dynstrNameOffset)) // sh_name
	binary.Write(buf, binary.LittleEndian, uint32(3))                // sh_type: SHT_STRTAB
	binary.Write(buf, binary.LittleEndian, uint64(2))                // sh_flags: SHF_ALLOC
	binary.Write(buf, binary.LittleEndian, uint64(strtabOffset))     // sh_addr
	binary.Write(buf, binary.LittleEndian, uint64(strtabOffset))     // sh_offset
	binary.Write(buf, binary.LittleEndian, uint64(len(strtab)))      // sh_size
	binary.Write(buf, binary.LittleEndian, uint32(0))                // sh_link
	binary.Write(buf, binary.LittleEndian, uint32(0))                // sh_info
	binary.Write(buf, binary.LittleEndian, uint64(1))                // sh_addralign
	binary.Write(buf, binary.LittleEndian, uint64(0))                // sh_entsize

	// Section 2: .dynamic
	binary.Write(buf, binary.LittleEndian, uint32(dynamicNameOffset)) // sh_name
	binary.Write(buf, binary.LittleEndian, uint32(6))                 // sh_type: SHT_DYNAMIC
	binary.Write(buf, binary.LittleEndian, uint64(3))                 // sh_flags: SHF_WRITE | SHF_ALLOC
	binary.Write(buf, binary.LittleEndian, uint64(dynOffset))         // sh_addr
	binary.Write(buf, binary.LittleEndian, uint64(dynOffset))         // sh_offset
	binary.Write(buf, binary.LittleEndian, uint64(dynSize))           // sh_size
	binary.Write(buf, binary.LittleEndian, uint32(1))                 // sh_link: points to .dynstr (index 1)
	binary.Write(buf, binary.LittleEndian, uint32(0))                 // sh_info
	binary.Write(buf, binary.LittleEndian, uint64(8))                 // sh_addralign
	binary.Write(buf, binary.LittleEndian, uint64(16))                // sh_entsize

	// Section 3: .shstrtab
	binary.Write(buf, binary.LittleEndian, uint32(shstrtabNameOffset)) // sh_name
	binary.Write(buf, binary.LittleEndian, uint32(3))                  // sh_type: SHT_STRTAB
	binary.Write(buf, binary.LittleEndian, uint64(0))                  // sh_flags
	binary.Write(buf, binary.LittleEndian, uint64(0))                  // sh_addr
	binary.Write(buf, binary.LittleEndian, uint64(shstrtabOffset))     // sh_offset
	binary.Write(buf, binary.LittleEndian, uint64(len(shstrtab)))      // sh_size
	binary.Write(buf, binary.LittleEndian, uint32(0))                  // sh_link
	binary.Write(buf, binary.LittleEndian, uint32(0))                  // sh_info
	binary.Write(buf, binary.LittleEndian, uint64(1))                  // sh_addralign
	binary.Write(buf, binary.LittleEndian, uint64(0))                  // sh_entsize

	return buf.Bytes()
}

func createZipWithFiles(files map[string][]byte) *zip.Reader {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write(content); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		panic(err)
	}
	return zr
}

func TestExtractWheelElfDependencies(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
		want  []DependencyIdentifier
	}{
		{
			name: "ExtensionWithCustomLibrariesAndStandardLibraries",
			files: map[string][]byte{
				"pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so": createSyntheticELF([]string{
					"libcgraph.so.6",
					"libcdt.so.5",
					"libc.so.6",
					"libm.so.6",
					"libpthread.so.0",
					"ld-linux-x86-64.so.2",
				}),
			},
			want: []DependencyIdentifier{
				{Namespace: NamespaceSoname, Name: "libcgraph.so.6", Provenance: "wheel:dt_needed:pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so"},
				{Namespace: NamespaceCLib, Name: "cgraph", Provenance: "wheel:dt_needed:pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so"},
				{Namespace: NamespaceSoname, Name: "libcdt.so.5", Provenance: "wheel:dt_needed:pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so"},
				{Namespace: NamespaceCLib, Name: "cdt", Provenance: "wheel:dt_needed:pygraphviz/_graphviz.cpython-310-x86_64-linux-gnu.so"},
			},
		},
		{
			name: "BundledAuditwheelLibsDirectory",
			files: map[string][]byte{
				"package/_ext.so": createSyntheticELF([]string{
					"libcustom-12345678.so.1",
					"libc.so.6",
				}),
				"package.libs/libcustom-12345678.so.1.0.0": []byte("not elf content"),
			},
			want: []DependencyIdentifier{
				{Namespace: NamespaceSoname, Name: "libcustom-12345678.so.1", Provenance: "wheel:dt_needed:package/_ext.so"},
				{Namespace: NamespaceCLib, Name: "custom", Provenance: "wheel:dt_needed:package/_ext.so"},
				{Namespace: NamespaceSoname, Name: "libcustom.so.1", Provenance: "wheel:libs:package.libs/libcustom-12345678.so.1.0.0"},
			},
		},
		{
			name: "PurePythonWheelNoElfFiles",
			files: map[string][]byte{
				"pkg/__init__.py": []byte("print('hello')\n"),
			},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			zr := createZipWithFiles(tc.files)
			got, err := ExtractWheelElfDependencies(zr)
			if err != nil {
				t.Fatalf("ExtractWheelElfDependencies() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExtractWheelElfDependencies() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractCLibStem(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"StandardSoname", "libcgraph.so.6", "cgraph"},
		{"StandardSharedLib", "libcdt.so", "cdt"},
		{"AuditwheelHashedSo", "libcgraph-77b31b67.so.6.0.0", "cgraph"},
		{"NonLibPrefix", "somefile.so", ""},
		{"NoSoExtension", "libcgraph.a", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCLibStem(tc.in)
			if got != tc.want {
				t.Errorf("extractCLibStem(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
