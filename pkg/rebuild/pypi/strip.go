// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/google/shlex"
	bash "github.com/msuozzo/bonsai/bonsai-bash"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

type elfStripSummary struct {
	elfCount            int
	anyDebug            bool
	anySymtab           bool
	bundledKeepsSymbols bool // any "<distribution>.libs/" or "<pkg>/.libs/" ELF retains DWARF or .symtab
}

// inferStripMode selects a wheel-wide ELF strip mode when every non-bundled
// shared object in zr lacks DWARF sections. Candidates are tried in order:
//  1. Explicit cibuildwheel "repair-wheel-command" or "environment" flags in
//     "pyproject.toml", validated against the ELF symbol table state.
//  2. auditwheel "--strip" when neither extensions nor bundled libraries retain
//     symbols and the package build does not already strip binaries itself.
func inferStripMode(zr *zip.Reader, tree *git2.Tree, searchDir string, tags WheelTags, libc platform.LibcImpl, reqs []string) (StripMode, error) {
	summary, err := wheelELFStripSummary(zr)
	if err != nil {
		return "", err
	}
	// Mixed-DWARF wheels such as greenlet keep debug sections in at least one
	// extension module, so a blanket wheel-wide strip would corrupt them.
	if summary.elfCount == 0 || summary.anyDebug {
		return "", nil
	}
	ciCfg, err := extractCIStripConfig(tree, searchDir, tags, libc)
	if err != nil {
		return "", err
	}
	if mode, ok := ciCfg.matchMode(summary); ok {
		return mode, nil
	}
	if summary.anySymtab || summary.bundledKeepsSymbols {
		return "", nil
	}
	selfStrips, err := packageSelfStrips(tree, searchDir, reqs)
	if err != nil {
		return "", err
	}
	if selfStrips {
		return "", nil
	}
	return StripModeAuditwheel, nil
}

func wheelELFStripSummary(zr *zip.Reader) (elfStripSummary, error) {
	var summary elfStripSummary
	if zr == nil {
		return summary, nil
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !sharedLibNamePat.MatchString(strings.ToLower(path.Base(f.Name))) {
			continue
		}
		// An entry above maxELFFileSize leaves the wheel's strip state
		// unknown, so abstain rather than guessing a wheel-wide mode.
		if f.UncompressedSize64 > maxELFFileSize {
			return elfStripSummary{}, nil
		}
		rc, err := f.Open()
		if err != nil {
			return elfStripSummary{}, errors.Wrapf(err, "opening %s in wheel", f.Name)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxELFFileSize+1))
		rc.Close()
		if err != nil {
			return elfStripSummary{}, errors.Wrapf(err, "reading %s in wheel", f.Name)
		}
		if int64(len(data)) > maxELFFileSize {
			return elfStripSummary{}, nil
		}
		ef, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			// Non-ELF files matching "*.so" (such as GNU ld scripts) are not compiled objects.
			continue
		}
		hasDebug, hasSymtab := elfHasSymbols(ef)
		ef.Close()
		if isBundledLibPath(f.Name) {
			// NOTE: auditwheel "--strip" strips grafted "<distribution>.libs/"
			// and "<pkg>/.libs/" objects alongside extensions, so retained
			// symbols rule it out in inferStripMode and repairCommandStripMode.
			if hasDebug || hasSymtab {
				summary.bundledKeepsSymbols = true
			}
			continue
		}
		summary.elfCount++
		summary.anyDebug = summary.anyDebug || hasDebug
		summary.anySymtab = summary.anySymtab || hasSymtab
	}
	return summary, nil
}

func elfHasSymbols(ef *elf.File) (hasDebug, hasSymtab bool) {
	for _, sec := range ef.Sections {
		if strings.HasPrefix(sec.Name, ".debug_") || strings.HasPrefix(sec.Name, ".zdebug_") || sec.Name == ".gnu_debuglink" {
			hasDebug = true
		}
		if sec.Name == ".symtab" || sec.Type == elf.SHT_SYMTAB {
			hasSymtab = true
		}
	}
	return hasDebug, hasSymtab
}

type ciStripConfig struct {
	repairCmds []string
	ldflags    []string
	cflags     []string
}

func extractCIStripConfig(tree *git2.Tree, searchDir string, tags WheelTags, libc platform.LibcImpl) (ciStripConfig, error) {
	var out ciStripConfig
	if tree == nil {
		return out, nil
	}
	cfg, ok, err := readCibuildwheelConfig(tree, path.Clean(path.Join(searchDir, "pyproject.toml")))
	if err != nil || !ok {
		return ciStripConfig{}, err
	}
	out.collectOptions(cfg.CibuildwheelOptions)
	out.collectOptions(cfg.Linux)
	for _, ov := range cfg.Overrides {
		if overrideAppliesToTarget(ov.Select, tags, libc) {
			out.collectOptions(ov.CibuildwheelOptions)
		}
	}
	return out, nil
}

func (c *ciStripConfig) collectOptions(opts parsing.CibuildwheelOptions) {
	c.repairCmds = append(c.repairCmds, tomlStrings(opts.RepairWheelCommand)...)
	c.collectEnv(opts.Environment)
}

// collectEnv records the linker and compiler flags in env, which is either a
// cibuildwheel environment table or a shell assignment string such as
// "CFLAGS='-O3 -g0' LDFLAGS='-Wl,-s'".
func (c *ciStripConfig) collectEnv(env any) {
	var vars map[string]any
	switch v := env.(type) {
	case map[string]any:
		vars = v
	case string:
		words, err := shlex.Split(v)
		if err != nil {
			// Malformed shell quoting in an environment string is a rejected candidate.
			return
		}
		vars = make(map[string]any, len(words))
		for _, word := range words {
			if key, val, ok := strings.Cut(word, "="); ok && key != "" {
				vars[key] = val
			}
		}
	default:
		return
	}
	if s, ok := vars["LDFLAGS"].(string); ok && s != "" {
		c.ldflags = append(c.ldflags, s)
	}
	for _, key := range []string{"CFLAGS", "CXXFLAGS"} {
		if s, ok := vars[key].(string); ok && s != "" {
			c.cflags = append(c.cflags, s)
		}
	}
}

func (c *ciStripConfig) matchMode(summary elfStripSummary) (StripMode, bool) {
	for _, cmd := range c.repairCmds {
		if mode, ok := repairCommandStripMode(cmd, summary); ok {
			return mode, true
		}
	}
	for _, flags := range c.ldflags {
		stripsAll, stripsDebug := parseLinkerStripFlags(flags)
		if stripsAll && !summary.anySymtab {
			return StripModeLinkerAll, true
		}
		if stripsDebug && summary.anySymtab {
			return StripModeDebug, true
		}
	}
	for _, flags := range c.cflags {
		if compilerDisablesDebug(flags) && summary.anySymtab {
			return StripModeDebug, true
		}
	}
	return "", false
}

func repairCommandStripMode(script string, summary elfStripSummary) (StripMode, bool) {
	src := []byte(script)
	root, err := bash.NewParser().Parse(src)
	if err != nil {
		// An unparseable shell snippet in "repair-wheel-command" is a rejected candidate.
		return "", false
	}
	trailing := trailingArgs(root, src)
	for cmd := range root.Find(bash.KindCommand) {
		argv := append(commandArgv(cmd, src), trailing[cmd]...)
		if len(argv) == 0 {
			continue
		}
		name := path.Base(argv[0])
		if name == "auditwheel" && slices.Contains(argv[1:], "repair") && slices.Contains(argv[1:], "--strip") && !summary.anySymtab && !summary.bundledKeepsSymbols {
			return StripModeAuditwheel, true
		}
		if name == "strip" || strings.HasSuffix(name, "-strip") {
			debugOnly := slices.Contains(argv[1:], "--strip-debug") || slices.Contains(argv[1:], "-g") || slices.Contains(argv[1:], "-S")
			if debugOnly && summary.anySymtab {
				return StripModeDebug, true
			}
			if !debugOnly && !summary.anySymtab && !summary.bundledKeepsSymbols {
				return StripModeAuditwheel, true
			}
		}
	}
	return "", false
}

func parseLinkerStripFlags(flags string) (stripsAll, stripsDebug bool) {
	for _, tok := range strings.Fields(flags) {
		parts := []string{tok}
		if after, ok := strings.CutPrefix(tok, "-Wl,"); ok {
			parts = strings.Split(after, ",")
		}
		for _, part := range parts {
			switch part {
			case "-s", "--strip-all":
				stripsAll = true
			case "-S", "--strip-debug":
				stripsDebug = true
			}
		}
	}
	return stripsAll, stripsDebug
}

func compilerDisablesDebug(flags string) bool {
	lastG := ""
	for _, tok := range strings.Fields(flags) {
		if tok == "-g" || tok == "-g0" || tok == "-g1" || tok == "-g2" || tok == "-g3" || strings.HasPrefix(tok, "-ggdb") || strings.HasPrefix(tok, "-gdwarf") {
			lastG = tok
		}
	}
	return lastG == "-g0"
}

// packageSelfStrips reports whether the package's own PEP 517 build strips
// shared objects without an external "--strip" or linker override.
func packageSelfStrips(tree *git2.Tree, searchDir string, reqs []string) (bool, error) {
	// scikit-build-core sets "wheel.strip = true" by default, CMake Release
	// targets strip pybind11 modules, and Rust builders honor Cargo strip rules.
	if hasRequirement(reqs, "scikit-build-core", "cmake", "maturin", "setuptools-rust") {
		return true, nil
	}
	if tree == nil {
		return false, nil
	}
	setupPath := path.Clean(path.Join(searchDir, "setup.py"))
	f, err := tree.File(setupPath)
	if errors.Is(err, git2.ErrFileNotFound) {
		return false, nil
	} else if err != nil {
		return false, errors.Wrapf(err, "finding %s", setupPath)
	}
	contents, err := f.Contents()
	if err != nil {
		return false, errors.Wrapf(err, "reading %s", setupPath)
	}
	if strings.Contains(contents, "--strip-all") || strings.Contains(contents, "-Wl,-s") || strings.Contains(contents, "--strip-unneeded") {
		return true, nil
	}
	return strings.Contains(contents, "CMakeExtension") || (strings.Contains(contents, "cmake") && strings.Contains(contents, "--build")), nil
}
