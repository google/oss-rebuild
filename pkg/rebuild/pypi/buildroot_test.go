// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
)

// elfWithUnits returns a minimal ELF shared object whose DWARF debug info holds
// the given compile units, or a stripped one if there are none.
func elfWithUnits(units ...compileUnit) []byte {
	type section struct {
		name string
		typ  elf.SectionType
		data []byte
	}
	sections := []section{{name: ".shstrtab", typ: elf.SHT_STRTAB}}
	if len(units) > 0 {
		// A single abbreviation declares compile units without children whose
		// name and comp_dir are inline strings.
		const formString = 0x08
		abbrev := []byte{1, byte(dwarf.TagCompileUnit), 0, byte(dwarf.AttrName), formString, byte(dwarf.AttrCompDir), formString, 0, 0, 0}
		var info []byte
		for _, u := range units {
			die := slices.Concat([]byte{1}, []byte(u.Name), []byte{0}, []byte(u.CompDir), []byte{0})
			// DWARF 4 unit header: unit_length, version, debug_abbrev_offset and address_size.
			info = binary.LittleEndian.AppendUint32(info, uint32(7+len(die)))
			info = binary.LittleEndian.AppendUint16(info, 4)
			info = binary.LittleEndian.AppendUint32(info, 0)
			info = append(info, 8)
			info = append(info, die...)
		}
		sections = append(sections, section{".debug_abbrev", elf.SHT_PROGBITS, abbrev}, section{".debug_info", elf.SHT_PROGBITS, info})
	}
	nameOffsets := make([]uint32, len(sections))
	shstrtab := []byte{0}
	for i, s := range sections {
		nameOffsets[i] = uint32(len(shstrtab))
		shstrtab = append(append(shstrtab, s.name...), 0)
	}
	sections[0].data = shstrtab
	headerSize := binary.Size(elf.Header64{})
	var data []byte
	headers := []elf.Section64{{}}
	for i, s := range sections {
		headers = append(headers, elf.Section64{
			Name:      nameOffsets[i],
			Type:      uint32(s.typ),
			Off:       uint64(headerSize + len(data)),
			Size:      uint64(len(s.data)),
			Addralign: 1,
		})
		data = append(data, s.data...)
	}
	header := elf.Header64{
		Type:      uint16(elf.ET_DYN),
		Machine:   uint16(elf.EM_X86_64),
		Version:   uint32(elf.EV_CURRENT),
		Shoff:     uint64(headerSize + len(data)),
		Ehsize:    uint16(headerSize),
		Shentsize: uint16(binary.Size(elf.Section64{})),
		Shnum:     uint16(len(headers)),
		Shstrndx:  1,
	}
	copy(header.Ident[:], elf.ELFMAG)
	header.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	out := must(binary.Append(nil, binary.LittleEndian, header))
	out = append(out, data...)
	return must(binary.Append(out, binary.LittleEndian, headers))
}

func readWheel(t *testing.T, entries ...archive.ZipEntry) *zip.Reader {
	t.Helper()
	whl := wheelZip(t, entries)
	return must(zip.NewReader(bytes.NewReader(whl), int64(len(whl))))
}

func TestCompileUnits(t *testing.T) {
	speedups := compileUnit{Name: "pkg/_speedups.c", CompDir: "/project"}
	parser := compileUnit{Name: "pkg/_parser.c", CompDir: "/project"}
	zlib := compileUnit{Name: "deflate.c", CompDir: "/tmp/zlib-1.3.1"}
	tests := []struct {
		name        string
		entries     []archive.ZipEntry
		want        []compileUnit
		wantSkipped int
	}{
		{
			name: "ExtensionModules",
			entries: []archive.ZipEntry{
				zipEntry("pkg/__init__.py", ""),
				zipEntry("pkg/_speedups.cpython-312-x86_64-linux-gnu.so", string(elfWithUnits(speedups))),
				zipEntry("pkg/_parser.abi3.so", string(elfWithUnits(parser, speedups))),
			},
			want: []compileUnit{speedups, parser, speedups},
		},
		{
			name: "VendoredLibraries",
			entries: []archive.ZipEntry{
				zipEntry("pkg.libs/libz-1a2b3c4d.so", string(elfWithUnits(zlib))),
				zipEntry("pkg/.libs/libz.so", string(elfWithUnits(zlib))),
			},
			want: nil,
		},
		{
			name:    "StrippedModule",
			entries: []archive.ZipEntry{zipEntry("pkg/_speedups.cpython-312-x86_64-linux-gnu.so", string(elfWithUnits()))},
			want:    nil,
		},
		{
			name:    "NotELF",
			entries: []archive.ZipEntry{zipEntry("pkg/_speedups.so", "INPUT(libspeedups.so.1)\n")},
			want:    nil,
		},
		{
			name: "MalformedModule",
			entries: []archive.ZipEntry{
				zipEntry("pkg/_broken.so", "\x7fELF\x02\x01"),
				zipEntry("pkg/_speedups.cpython-312-x86_64-linux-gnu.so", string(elfWithUnits(speedups))),
			},
			want:        []compileUnit{speedups},
			wantSkipped: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped := compileUnits(readWheel(t, tc.entries...))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("compileUnits() returned diff (-want +got):\n%s", diff)
			}
			if len(skipped) != tc.wantSkipped {
				t.Errorf("compileUnits() skipped %d modules, want %d: %v", len(skipped), tc.wantSkipped, skipped)
			}
		})
	}
}

func TestVoteBuildRoot(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		units []compileUnit
		want  string
	}{
		{
			name:  "RelativeName",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "src/module.c", CompDir: "/project"}},
			want:  "/project",
		},
		{
			name:  "PackageInSubdirectory",
			files: []string{"python/src/module.c"},
			units: []compileUnit{{Name: "src/module.c", CompDir: "/project/python"}},
			want:  "/project",
		},
		{
			// CMake passes absolute paths and may compile outside the project.
			name:  "AbsoluteName",
			files: []string{"src/module.cpp"},
			units: []compileUnit{{Name: "/project/src/module.cpp", CompDir: "/tmp/tmpk2j3/build"}},
			want:  "/project",
		},
		{
			// Meson compiles in a build directory within the project.
			name:  "ParentRelativeName",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "../../src/module.c", CompDir: "/project/build/cp312"}},
			want:  "/project",
		},
		{
			name:  "LongestRepositoryPath",
			files: []string{"module.c", "python/module.c"},
			units: []compileUnit{{Name: "module.c", CompDir: "/project/python"}},
			want:  "/project",
		},
		{
			name:  "CythonSource",
			files: []string{"pkg/_parser.pyx"},
			units: []compileUnit{{Name: "pkg/_parser.c", CompDir: "/project"}},
			want:  "/project",
		},
		{
			name:  "CythonPurePythonSource",
			files: []string{"pkg/_compiled.py"},
			units: []compileUnit{{Name: "/tmp/build/pkg/_compiled.cpp", CompDir: "/tmp/build"}},
			want:  "/tmp/build",
		},
		{
			// mypyc writes C files to build directories that mirror the package.
			name:  "GeneratedInBuildDirectory",
			files: []string{"pkg/mod.py"},
			units: []compileUnit{
				{Name: "build/pkg/mod.c", CompDir: "/project"},
				{Name: "/tmp/tmpa1b2/build/pkg/mod.c", CompDir: "/project"},
			},
			want: "",
		},
		{
			// The musl C runtime that the toolchain links in is not in the repository.
			name:  "ToolchainUnit",
			files: []string{"src/module.c"},
			units: []compileUnit{
				{Name: "crt/x86_64/crti.s", CompDir: "/home/buildozer/aports/main/musl/src/musl-1.2.5"},
				{Name: "src/module.c", CompDir: "/project"},
			},
			want: "/project",
		},
		{
			name:  "LTOUnit",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "<artificial>", CompDir: "/project"}},
			want:  "",
		},
		{
			name:  "RustUnit",
			files: []string{"src/lib.rs"},
			units: []compileUnit{{Name: "src/lib.rs/@/ext.1a2b3c4d-cgu.0", CompDir: "/io"}},
			want:  "/io",
		},
		{
			name:  "RustCrateInSubdirectory",
			files: []string{"rust/src/lib.rs"},
			units: []compileUnit{{Name: "src/lib.rs/@/ext.1a2b3c4d-cgu.0", CompDir: "/io/rust"}},
			want:  "/io",
		},
		{
			name:  "RustDependencyUnit",
			files: []string{"src/lib.rs"},
			units: []compileUnit{{Name: "/root/.cargo/registry/src/index.crates.io-1949cf8c6b5b557f/pyo3-0.22.0/src/lib.rs/@/pyo3.5e6f7a8b-cgu.0", CompDir: "/io"}},
			want:  "",
		},
		{
			name:  "NoCompDir",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "src/module.c"}},
			want:  "",
		},
		{
			name:  "Majority",
			files: []string{"src/a.c", "src/b.c"},
			units: []compileUnit{
				{Name: "src/a.c", CompDir: "/project"},
				{Name: "src/b.c", CompDir: "/project"},
				{Name: "src/a.c", CompDir: "/opt/vendor"},
			},
			want: "/project",
		},
		{
			name:  "Tie",
			files: []string{"module.c"},
			units: []compileUnit{
				{Name: "module.c", CompDir: "/tmp/build"},
				{Name: "module.c", CompDir: "/project"},
			},
			want: "/project",
		},
		{
			name:  "UnusableRootLosesVote",
			files: []string{"src/module.c"},
			units: []compileUnit{
				{Name: "src/module.c", CompDir: "/home/runner/my project"},
				{Name: "src/module.c", CompDir: "/home/runner/my project"},
				{Name: "src/module.c", CompDir: "/project"},
			},
			want: "/project",
		},
		{
			name:  "UnusableRootOnly",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "src/module.c", CompDir: "/usr/local"}},
			want:  "",
		},
		{
			name:  "CheckoutRoot",
			files: []string{"src/module.c"},
			units: []compileUnit{{Name: "src/module.c", CompDir: "/src"}},
			want:  "/src",
		},
		{
			name:  "NoUnits",
			files: []string{"src/module.c"},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := make(map[string]bool)
			for _, f := range tc.files {
				files[f] = true
			}
			if got := voteBuildRoot(tc.units, files); got != tc.want {
				t.Errorf("voteBuildRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}
