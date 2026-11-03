// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Build root inference reads the path that upstream built the repository at
// from the DWARF debug info or .rodata strings of a wheel's extension modules.
//
// Each compile unit (DWARF 4 §3.1.1 DW_TAG_compile_unit) records its primary
// source file in DW_AT_name and the working directory of the compiler in
// DW_AT_comp_dir. Their form depends on the toolchain:
//   - C and C++ (setuptools, meson-python): DW_AT_name is relative to
//     DW_AT_comp_dir, which is the project root or a "build/<abi>"
//     subdirectory.
//   - C and C++ (CMake): DW_AT_name is absolute, and DW_AT_comp_dir may be an
//     out-of-tree build directory.
//   - Rust (maturin, setuptools-rust): DW_AT_name of a workspace crate is
//     "<crate-root>/@/<cgu>" relative to DW_AT_comp_dir. External crates have
//     absolute paths under ".cargo".
//   - Generated C and C++ (Cython, mypyc): DW_AT_name is a generated file that
//     need not be in the repository, though its .pyx or .py source is.
//   - Toolchain and Link-Time Optimization (LTO) units: C runtime startup
//     objects and "<artificial>" LTO partitions name no file in the
//     repository and drop out.
//
// Stripped modules and CMake Release builds without "-g" have no .debug_info
// section, yet still embed NUL-terminated absolute __FILE__ paths from
// assertions and logging macros in .rodata.
//
// inferBuildRoot resolves these in three steps:
//  1. Collection: Reads the DWARF compile units and NUL-terminated .rodata C
//     and C++ source paths of each .so member up to maxModuleSize, except the
//     libraries that auditwheel grafts into "<distribution>.libs/" or
//     "<pkg>/.libs/". Rust .rs paths in .rodata are excluded because rustc
//     records relative paths for workspace crates and absolute paths only for
//     external Cargo dependencies. A module or section that cannot be read is
//     skipped so it does not hide the root recorded by the others.
//  2. Matching: Resolves each DW_AT_name against DW_AT_comp_dir, stripping the
//     rustc "/@/<cgu>" suffix from relative names first. The longest suffix of
//     the resulting path that names a file in the repository is the
//     repository-relative path, and the prefix before it is the unit's
//     candidate root. Taking the longest suffix keeps a top-level file from
//     shadowing a file of the same name in a subdirectory. A generated .c or
//     .cpp file also matches through its .pyx or .py sibling. Relative names
//     and generated files count only if DW_AT_comp_dir lies inside the
//     candidate root, which rejects mypyc and Cython build_dir outputs written
//     to build directories that mirror the package layout. When DWARF yields no
//     usable root, .rodata paths are matched by longest suffix with stricter
//     rules: because .rodata lacks DW_AT_comp_dir and includes strings from all
//     linked headers, the repository-relative path must span a directory (so
//     top-level names like "config.h" cannot match unrelated headers) and
//     generated .pyx or .py sources do not match.
//  3. Selection: Candidates drop out if validateBuildRoot rejects them (such as
//     paths that need shell quoting or lie inside "/src"), if they lie inside
//     "site-packages", "dist-packages", or ".cargo" (unpacked dependencies), if
//     they name a pre-existing directory in the build image such as "/tmp",
//     "/var/tmp", or "/usr/src" (where "mkdir" without "-p" fails, though fresh
//     subdirectories like "/tmp/build" and "/usr/src/app" work), or if they
//     collide with OS, toolchain, or rebuild-harness paths such as
//     "/usr/local", "/opt/rh", "/build", or "/deps". The most common candidate
//     wins, breaking ties by the lexically smallest root. checkoutDir ("/src")
//     stays a candidate so an upstream build in the checkout can be told apart
//     from a wheel with no evidence, and then maps to "" since the build
//     already runs there.
//
// References:
//   - DWARF Debugging Information Format Version 4, §3.1.1:
//     https://dwarfstd.org/doc/DWARF4.pdf
//   - auditwheel wheel repair layout:
//     https://github.com/pypa/auditwheel
//   - rustc codegen-unit DW_AT_name suffix:
//     https://github.com/rust-lang/rust/issues/88521

package pypi

import (
	"archive/zip"
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

// maxModuleSize bounds the extension modules that are read into memory to
// parse their ELF sections. It admits common extension modules while skipping
// the largest libraries, such as those of torch.
const maxModuleSize = 256 << 20

type compileUnit struct {
	Name    string // primary source file as passed to the compiler (DW_AT_name)
	CompDir string // working directory of the compiler (DW_AT_comp_dir)
}

// sourceUnits returns the DWARF compile units and .rodata source paths of the
// wheel's extension modules, recording an error in skipped for each module or
// section that cannot be read.
func sourceUnits(zr *zip.Reader) (units []compileUnit, rodataPaths []string, skipped []error) {
	for _, f := range zr.File {
		if !isExtensionModule(f.Name) || f.UncompressedSize64 > maxModuleSize {
			continue
		}
		ef, err := openELF(f)
		if err != nil {
			skipped = append(skipped, errors.Wrapf(err, "reading %s", f.Name))
			continue
		}
		du, err := moduleCompileUnits(ef)
		if err != nil {
			skipped = append(skipped, errors.Wrapf(err, "reading debug info of %s", f.Name))
		}
		units = append(units, du...)
		rp, err := moduleRodataPaths(ef)
		if err != nil {
			skipped = append(skipped, errors.Wrapf(err, "reading .rodata of %s", f.Name))
		}
		rodataPaths = append(rodataPaths, rp...)
	}
	return units, rodataPaths, skipped
}

// isExtensionModule reports whether name is a shared object outside the
// "<distribution>.libs" and "<pkg>/.libs" directories where auditwheel vendors
// external libraries.
func isExtensionModule(name string) bool {
	return strings.HasSuffix(name, ".so") && !strings.Contains(path.Dir(name)+"/", ".libs/")
}

var errNotELF = errors.New("not an ELF file")

// openELF parses f as an ELF file, returning errNotELF if f is not an ELF file.
func openELF(f *zip.File) (*elf.File, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, errors.Wrap(err, "opening module")
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, errors.Wrap(err, "reading module")
	}
	if !bytes.HasPrefix(b, []byte(elf.ELFMAG)) {
		return nil, errNotELF
	}
	ef, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return nil, errors.Wrap(err, "parsing ELF")
	}
	return ef, nil
}

// moduleCompileUnits returns the compile units in the debug info of ef, or
// nil, nil if ef is stripped.
func moduleCompileUnits(ef *elf.File) ([]compileUnit, error) {
	// NOTE: ef.DWARF returns an error when debug sections are absent, so
	// checking for them first distinguishes stripped modules from malformed
	// debug info.
	if ef.Section(".debug_info") == nil && ef.Section(".zdebug_info") == nil {
		return nil, nil
	}
	d, err := ef.DWARF()
	if err != nil {
		return nil, errors.Wrap(err, "loading DWARF")
	}
	var units []compileUnit
	r := d.Reader()
	for {
		e, err := r.Next()
		if err != nil {
			return nil, errors.Wrap(err, "reading DWARF")
		}
		if e == nil {
			return units, nil
		}
		if e.Tag == dwarf.TagCompileUnit {
			name, _ := e.Val(dwarf.AttrName).(string)
			compDir, _ := e.Val(dwarf.AttrCompDir).(string)
			units = append(units, compileUnit{Name: name, CompDir: compDir})
		}
		r.SkipChildren()
	}
}

// moduleRodataPaths returns the NUL-terminated C and C++ source paths in the
// .rodata section of ef.
func moduleRodataPaths(ef *elf.File) ([]string, error) {
	sec := ef.Section(".rodata")
	if sec == nil {
		return nil, nil
	}
	b, err := sec.Data()
	if err != nil {
		return nil, errors.Wrap(err, "reading .rodata")
	}
	var paths []string
	for len(b) > 0 {
		var entry []byte
		entry, b, _ = bytes.Cut(b, []byte{0})
		if isSourcePath(entry) {
			paths = append(paths, string(entry))
		}
	}
	return paths, nil
}

// isSourcePath reports whether b is an absolute C or C++ source or header path
// made of characters valid in a build root.
func isSourcePath(b []byte) bool {
	switch path.Ext(string(b)) {
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx":
		return buildRootPat.Match(b)
	default:
		return false
	}
}

func repoFiles(tree *git2.Tree) (map[string]bool, error) {
	files := make(map[string]bool)
	err := tree.Paths(func(p string, _ git2.FileMode, _ git2.Hash) error {
		files[p] = true
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "walking tree")
	}
	return files, nil
}

// mostCommonBuildRoot returns the usable candidate root that the most compile
// units agree on, breaking ties by the lexically smallest root.
func mostCommonBuildRoot(units []compileUnit, files map[string]bool) string {
	roots := make([]string, 0, len(units))
	for _, u := range units {
		roots = append(roots, u.buildRoot(files))
	}
	return mostCommonRoot(roots)
}

// mostCommonRodataBuildRoot returns the usable candidate root that the most
// .rodata source paths agree on, breaking ties by the lexically smallest root.
func mostCommonRodataBuildRoot(paths []string, files map[string]bool) string {
	roots := make([]string, 0, len(paths))
	for _, p := range paths {
		roots = append(roots, rodataBuildRoot(p, files))
	}
	return mostCommonRoot(roots)
}

func mostCommonRoot(roots []string) string {
	counts := make(map[string]int)
	for _, root := range roots {
		if !isUsableBuildRoot(root) {
			continue
		}
		counts[root]++
	}
	var best string
	for root, n := range counts {
		if n > counts[best] || (n == counts[best] && root < best) {
			best = root
		}
	}
	return best
}

// isUsableBuildRoot reports whether root is a valid candidate for build root
// selection. checkoutDir stays a candidate so an upstream build in "/src" can
// be told apart from a wheel with no evidence.
func isUsableBuildRoot(root string) bool {
	if root == "" || isDependencyTree(root) || slices.Contains(imageDirs, root) {
		return false
	}
	if root != checkoutDir && validateBuildRoot(root) != nil {
		return false
	}
	for _, dir := range systemDirs {
		if root == dir || strings.HasPrefix(root, dir+"/") {
			return false
		}
	}
	return true
}

// imageDirs are directories that already exist in manylinux and musllinux
// images, so "mkdir" without "-p" fails if one is chosen as the build root,
// even though fresh subdirectories below them (such as "/tmp/build" or
// "/usr/src/app") work.
var imageDirs = []string{"/home", "/media", "/mnt", "/opt", "/root", "/srv", "/tmp", "/usr", "/usr/src", "/var", "/var/tmp"}

// systemDirs are paths and subtrees of the build container reserved by the OS,
// toolchains, interpreters, or the rebuild harness (such as the "/build"
// entrypoint script, "/timewarp", "/deps", and "/out").
var systemDirs = []string{
	"/bin",
	"/build",
	"/deps",
	"/dev",
	"/etc",
	"/lib",
	"/lib64",
	"/opt/_internal",
	"/opt/python",
	"/opt/rh",
	"/out",
	"/proc",
	"/run",
	"/sbin",
	"/sys",
	"/timewarp",
	"/usr/bin",
	"/usr/include",
	"/usr/lib",
	"/usr/lib64",
	"/usr/libexec",
	"/usr/local",
	"/usr/sbin",
	"/usr/share",
	"/var/cache",
	"/var/lib",
	"/var/log",
	"/var/run",
	"/var/spool",
}

// dependencyTreeDirs names the directories where pip and cargo unpack
// dependencies.
var dependencyTreeDirs = []string{"site-packages", "dist-packages", ".cargo"}

func isDependencyTree(root string) bool {
	return slices.ContainsFunc(strings.Split(root, "/"), func(dir string) bool {
		return slices.Contains(dependencyTreeDirs, dir)
	})
}

// rodataBuildRoot returns the prefix of p before its longest multi-segment
// suffix in files, or "" if no suffix matches.
func rodataBuildRoot(p string, files map[string]bool) string {
	p = path.Clean(p)
	for i := 1; i < len(p); i++ {
		if p[i] != '/' {
			continue
		}
		root, rel := p[:i], p[i+1:]
		if !strings.Contains(rel, "/") {
			return ""
		}
		if files[rel] {
			return root
		}
	}
	return ""
}

// buildRoot returns the prefix of the unit's source path before its longest
// suffix in files, or "" if no suffix matches.
func (u compileUnit) buildRoot(files map[string]bool) string {
	compDir := path.Clean(u.CompDir)
	src := u.Name
	if !path.IsAbs(src) {
		// NOTE: Strip the codegen-unit suffix that rustc appends to relative
		// crate roots (https://github.com/rust-lang/rust/issues/88521).
		src, _, _ = strings.Cut(src, "/@/")
		src = path.Join(compDir, src)
	}
	if !path.IsAbs(src) {
		return ""
	}
	src = path.Clean(src)
	for i := 1; i < len(src); i++ {
		if src[i] != '/' {
			continue
		}
		root, rel := src[:i], src[i+1:]
		// NOTE: Relative names and generated sources require CompDir inside
		// root so out-of-tree build directories that mirror the package layout
		// cannot match.
		inRoot := compDir == root || strings.HasPrefix(compDir, root+"/")
		if files[rel] && (inRoot || path.IsAbs(u.Name)) {
			return root
		}
		if inRoot && hasPythonSource(files, rel) {
			return root
		}
	}
	return ""
}

// hasPythonSource reports whether files contains a Cython or Python source
// next to the generated C or C++ file at rel.
func hasPythonSource(files map[string]bool, rel string) bool {
	ext := path.Ext(rel)
	if ext != ".c" && ext != ".cpp" {
		return false
	}
	stem := strings.TrimSuffix(rel, ext)
	return files[stem+".pyx"] || files[stem+".py"]
}
