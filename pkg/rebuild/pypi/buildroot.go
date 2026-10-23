// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Build root extraction from the Executable and Linkable Format (ELF)
// extension modules of a wheel.
//
// Build root inference splits across three files:
//   - `buildroot.go` extracts candidate roots from the wheel's ELF extension
//     modules and votes on them against the repository file tree at the
//     release commit.
//   - `strategy.go` defines `validateBuildRoot` and `checkoutDir` (`/src`).
//     Voting filters candidates through `validateBuildRoot` so paths the build
//     step cannot copy the checkout to (such as system directories or paths
//     needing shell quoting) cannot outvote usable roots. `checkoutDir` stays
//     a candidate during voting so an upstream build in `/src` can be told
//     apart from a wheel that carries no build root evidence.
//   - `infer.go` (`inferBuildRoot`) maps `checkoutDir` to `""` so a build that
//     already ran in `/src` does not copy the checkout.
//
// Extraction and voting follow these rules:
//   - Inspected modules: `.so` members up to `maxModuleSize`, excluding
//     shared libraries that `auditwheel` grafts into `<distribution>.libs/` or
//     `<pkg>/.libs/`. Modules whose debug info cannot be read are left out of
//     the vote and returned in `skipped` so one malformed module does not hide
//     the root recorded by the others.
//   - Compile unit paths (Debugging With Arbitrary Record Formats (DWARF) 4
//     §3.1.1 `DW_TAG_compile_unit`):
//       - C and C++ (`setuptools`, `meson-python`): `DW_AT_name` is relative
//         to `DW_AT_comp_dir` (the project root or a `build/<abi>`
//         subdirectory) and resolves to `<comp_dir>/<name>`.
//       - C and C++ (`CMake`): `DW_AT_name` is an absolute source path and
//         `DW_AT_comp_dir` may be an out-of-tree build directory.
//       - Rust (`maturin`, `setuptools-rust`): `rustc` sets `DW_AT_name` of
//         workspace crates to `<crate-root>/@/<cgu>` relative to
//         `DW_AT_comp_dir`, so the `/@/...` suffix is stripped before joining.
//         External crates have absolute `DW_AT_name` paths under `.cargo` and
//         drop out.
//       - Toolchain and Link-Time Optimization (LTO) units: C runtime startup
//         objects and `<artificial>` LTO partitions do not match `tree` and
//         drop out.
//   - Repository suffix matching: The longest suffix of the clean absolute
//     source path that names a file in `tree` is the repository-relative path,
//     and the prefix before it is the candidate build root. Taking the longest
//     suffix keeps a top-level file from shadowing a file of the same name in
//     a subdirectory.
//   - Generated C and C++ (`Cython`, `mypyc`): When a `.c` or `.cpp` suffix is
//     not in `tree`, a sibling `.pyx` or `.py` file in `tree` matches only if
//     `DW_AT_comp_dir` lies within the candidate root. This admits in-tree
//     `Cython` builds while rejecting `mypyc` and `Cython` `build_dir` outputs
//     written to build directories that mirror the package layout. Relative
//     `DW_AT_name` paths require the same containment.
//   - Voting: Candidate roots inside `site-packages`, `dist-packages`, or
//     `.cargo` hold unpacked dependencies rather than the repository and drop
//     out alongside roots rejected by `validateBuildRoot`. The root with the
//     most votes wins, breaking ties by the lexically smallest root.
//
// References:
//   - DWARF Debugging Information Format Version 4, §3.1.1:
//     https://dwarfstd.org/doc/DWARF4.pdf
//   - `auditwheel` wheel repair layout:
//     https://github.com/pypa/auditwheel
//   - `rustc` codegen-unit `DW_AT_name` suffix:
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

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

// maxModuleSize bounds the extension modules that are read into memory to
// parse their ELF sections. It admits common extension modules while skipping
// the largest libraries, such as those of `torch`.
const maxModuleSize = 256 << 20

type compileUnit struct {
	Name    string // primary source file as passed to the compiler (`DW_AT_name`)
	CompDir string // working directory of the compiler (`DW_AT_comp_dir`)
}

// buildRootFromDWARF returns the absolute path that upstream built the
// repository at, as recorded in the DWARF debug info of the wheel's extension
// modules, or `""` if no debug info refers to a file in `tree`. `skipped`
// holds an error for each module whose debug info could not be read.
func buildRootFromDWARF(zr *zip.Reader, tree *object.Tree) (root string, skipped []error, err error) {
	units, skipped := compileUnits(zr)
	if len(units) == 0 {
		return "", skipped, nil
	}
	files, err := repoFiles(tree)
	if err != nil {
		return "", skipped, errors.Wrap(err, "listing repository files")
	}
	return voteBuildRoot(units, files), skipped, nil
}

// compileUnits returns the DWARF compile units of the wheel's extension
// modules, recording an error in `skipped` for each module that cannot be read.
func compileUnits(zr *zip.Reader) (units []compileUnit, skipped []error) {
	for _, f := range zr.File {
		if !isExtensionModule(f.Name) || f.UncompressedSize64 > maxModuleSize {
			continue
		}
		moduleUnits, err := moduleCompileUnits(f)
		if err != nil {
			skipped = append(skipped, errors.Wrapf(err, "reading %s", f.Name))
			continue
		}
		units = append(units, moduleUnits...)
	}
	return units, skipped
}

// isExtensionModule reports whether `name` is a shared object outside the
// `<distribution>.libs` and `<pkg>/.libs` directories where `auditwheel`
// vendors external libraries.
func isExtensionModule(name string) bool {
	return strings.HasSuffix(name, ".so") && !strings.Contains(path.Dir(name)+"/", ".libs/")
}

// moduleCompileUnits returns the compile units in the debug info of `f`, or
// `nil, nil` if `f` is not an ELF file or is stripped.
func moduleCompileUnits(f *zip.File) ([]compileUnit, error) {
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
		return nil, nil
	}
	ef, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return nil, errors.Wrap(err, "parsing ELF")
	}
	// NOTE: `ef.DWARF` returns an error when debug sections are absent, so
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

func repoFiles(tree *object.Tree) (map[string]bool, error) {
	files := make(map[string]bool)
	w := object.NewTreeWalker(tree, true, nil)
	defer w.Close()
	for {
		name, entry, err := w.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, errors.Wrap(err, "walking tree")
		}
		if entry.Mode.IsFile() {
			files[name] = true
		}
	}
}

// voteBuildRoot returns the candidate root that the most compile units agree
// on, breaking ties by the lexically smallest root.
func voteBuildRoot(units []compileUnit, files map[string]bool) string {
	votes := make(map[string]int)
	for _, u := range units {
		root := u.buildRoot(files)
		if root == "" || isDependencyTree(root) || (root != checkoutDir && validateBuildRoot(root) != nil) {
			continue
		}
		votes[root]++
	}
	var best string
	for root, n := range votes {
		if n > votes[best] || (n == votes[best] && root < best) {
			best = root
		}
	}
	return best
}

// dependencyTreeDirs names the directories where `pip` and `cargo` unpack
// dependencies.
var dependencyTreeDirs = []string{"site-packages", "dist-packages", ".cargo"}

func isDependencyTree(root string) bool {
	return slices.ContainsFunc(strings.Split(root, "/"), func(dir string) bool {
		return slices.Contains(dependencyTreeDirs, dir)
	})
}

// buildRoot returns the prefix of `u`'s source path before its longest suffix
// in `files`, or `""` if no suffix matches.
func (u compileUnit) buildRoot(files map[string]bool) string {
	compDir := path.Clean(u.CompDir)
	src := u.Name
	if !path.IsAbs(src) {
		// NOTE: Strip the codegen-unit suffix that `rustc` appends to relative
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
		// NOTE: Relative names and generated sources require `CompDir` inside
		// `root` so out-of-tree build directories that mirror the package
		// layout cannot match.
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

// hasPythonSource reports whether `files` contains a `Cython` or Python source
// next to the generated C or C++ file at `rel`.
func hasPythonSource(files map[string]bool, rel string) bool {
	ext := path.Ext(rel)
	if ext != ".c" && ext != ".cpp" {
		return false
	}
	stem := strings.TrimSuffix(rel, ext)
	return files[stem+".pyx"] || files[stem+".py"]
}
