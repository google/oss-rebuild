// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"path"
	re "regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/semver"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/msuozzo/go-git2"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

// rustupVersion pins the archived rustup-init release fetched by pypi/install-rust
// from https://static.rust-lang.org/rustup/archive/<version>/<triple>/rustup-init.
const rustupVersion = "1.29.3"

type rustHost struct {
	triple       string
	rustupSHA256 string
}

var (
	// SHA-256 digests from https://static.rust-lang.org/rustup/archive/1.29.3/<triple>/rustup-init.sha256.
	rustHostX86_64GNU = rustHost{
		triple:       "x86_64-unknown-linux-gnu",
		rustupSHA256: "b1c8cb14e30889e7e2999e6b69e62d9e9b9ffc0f4df8180564d306d9680e6c55",
	}
	rustHostX86_64Musl = rustHost{
		triple:       "x86_64-unknown-linux-musl",
		rustupSHA256: "1552169aba2836777136ccff7a381e95c17233b5d8250cd5e10ae3cb971bfcfc",
	}
	// rustcVersionPat extracts the rustc version and any pre-release suffix stamped
	// into ELF .comment sections (e.g. "rustc version 1.97.1 (8bab26f4f 2026-07-14)").
	rustcVersionPat = re.MustCompile(`rustc (?:version )?(\d+\.\d+\.\d+)(-[\w.]+)?(?:\s|$)`)
	// toolchainChannelPat matches a numeric stable Rust release channel in
	// rust-toolchain(.toml), optionally suffixed with an <arch>-<vendor>-<os> target triple.
	toolchainChannelPat = re.MustCompile(`^(\d+\.\d+(?:\.\d+)?)(?:-[A-Za-z0-9_]+-[A-Za-z0-9_]+-[A-Za-z0-9_-]+)?$`)
	// stableChannelPat matches the floating "stable" channel in rust-toolchain(.toml),
	// optionally suffixed with an <arch>-<vendor>-<os> target triple.
	stableChannelPat = re.MustCompile(`^stable(?:-[A-Za-z0-9_]+-[A-Za-z0-9_]+-[A-Za-z0-9_-]+)?$`)
	// validRustVersionPat requires a fully pinned 3-component release before shell
	// interpolation. A major.minor channel would let rustup pick the newest patch at build time.
	validRustVersionPat = re.MustCompile(`^\d+\.\d+\.\d+$`)
	// validMaturinFeaturePat validates a Cargo feature identifier before shell interpolation.
	validMaturinFeaturePat = re.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)?$`)
)

// usesRust reports whether the wheel build requires a Rust toolchain.
func usesRust(reqs []string, tree *git2.Tree, dir string) (bool, error) {
	if hasRequirement(reqs, "maturin", "setuptools-rust") {
		return true, nil
	}
	return hasSetuptoolsRustSetupPy(tree, dir)
}

func hasSetuptoolsRustSetupPy(tree *git2.Tree, dir string) (bool, error) {
	if tree == nil {
		return false, nil
	}
	setupPath := path.Join(dir, "setup.py")
	f, err := tree.File(setupPath)
	if errors.Is(err, git2.ErrFileNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrapf(err, "looking up %s", setupPath)
	}
	content, err := f.Contents()
	if err != nil {
		return false, errors.Wrapf(err, "reading %s", setupPath)
	}
	return strings.Contains(content, "setuptools_rust"), nil
}

// usesMaturin reports whether the wheel was built with maturin.
func usesMaturin(reqs []string) bool {
	return hasRequirement(reqs, "maturin")
}

// inferMaturinOptions returns options for a direct maturin build. It returns nil
// when cibuildwheel drives maturin through PEP 517 and auditwheel, because the
// platform-wheel step already reproduces that path.
func inferMaturinOptions(tree *git2.Tree, dir string, tags WheelTags) (*MaturinOptions, error) {
	usesCibw, err := usesCibuildwheel(tree, dir)
	if err != nil {
		return nil, errors.Wrap(err, "detecting cibuildwheel configuration")
	}
	if usesCibw {
		return nil, nil
	}
	features, err := inferMaturinFeatures(tree, dir, tags)
	if err != nil {
		return nil, errors.Wrap(err, "inferring maturin features")
	}
	return &MaturinOptions{Features: features}, nil
}

// inferMaturinFeatures inspects pyproject.toml and Cargo.toml when building an
// abi3 wheel with maturin and selects a single non-default Cargo feature that
// enables pyo3's abi3 mode. Because passing "--features" on the CLI overrides
// "[tool.maturin] features" in pyproject.toml, any features configured there are
// preserved ahead of the inferred abi3 feature.
func inferMaturinFeatures(tree *git2.Tree, dir string, tags WheelTags) ([]string, error) {
	if !strings.Contains(tags.ABI, "abi3") {
		return nil, nil
	}
	maturinCfg, err := readMaturinConfig(tree, dir)
	if err != nil {
		return nil, err
	}
	pyprojFeatures, ok := maturinPyprojectFeatures(maturinCfg.Features)
	if !ok {
		// NOTE: Non-string entries such as conditional feature tables in
		// [tool.maturin] features are dropped by maturin when --features is
		// passed on the CLI, so leave pyproject.toml in control.
		return nil, nil
	}
	cf, err := findCargoManifest(tree, dir, strings.TrimSpace(maturinCfg.ManifestPath))
	if err != nil || cf == nil {
		return nil, err
	}
	content, err := cf.Contents()
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", cf.Name)
	}
	cargoToml, err := cratesio.ParseCargoTOML(content)
	if err != nil {
		// NOTE: Malformed syntax rejects this candidate rather than aborting inference.
		return nil, nil
	}
	for _, dep := range slices.Concat(cargoToml.Features["default"], pyprojFeatures) {
		if isPyO3ABI3FeatureDep(dep, tags.ABI) {
			return nil, nil
		}
		for _, subDep := range cargoToml.Features[dep] {
			if isPyO3ABI3FeatureDep(subDep, tags.ABI) {
				return nil, nil
			}
		}
	}
	feat, ok := selectABI3Feature(cargoToml.Features, tags)
	if !ok {
		return nil, nil
	}
	return append(slices.Clip(pyprojFeatures), feat), nil
}

// selectABI3Feature returns the Cargo feature that enables pyo3 abi3 for tags,
// preferring an exact Python version match, then an unversioned one, then any
// match, and only when that tier has a single candidate.
func selectABI3Feature(features map[string][]string, tags WheelTags) (string, bool) {
	targetPySuffix := targetPyO3VersionSuffix(tags.Python)
	var exactMatches, unversionedMatches, allMatches []string
	for feat, deps := range features {
		if feat == "default" || !validMaturinFeaturePat.MatchString(feat) {
			continue
		}
		var matched, exact, unversioned bool
		for _, dep := range deps {
			suffix, ok := parsePyO3ABI3FeatureDep(dep, tags.ABI)
			if !ok {
				continue
			}
			matched = true
			if targetPySuffix != "" && suffix == targetPySuffix {
				exact = true
			} else if suffix == "" {
				unversioned = true
			}
		}
		if matched {
			allMatches = append(allMatches, feat)
		}
		if exact {
			exactMatches = append(exactMatches, feat)
		} else if unversioned {
			unversionedMatches = append(unversionedMatches, feat)
		}
	}
	for _, candidates := range [][]string{exactMatches, unversionedMatches, allMatches} {
		if len(candidates) == 1 {
			return candidates[0], true
		}
	}
	return "", false
}

func readMaturinConfig(tree *git2.Tree, dir string) (pypiresolver.MaturinConfig, error) {
	if tree == nil {
		return pypiresolver.MaturinConfig{}, nil
	}
	pyprojPath := path.Join(dir, "pyproject.toml")
	pyf, err := tree.File(pyprojPath)
	if errors.Is(err, git2.ErrFileNotFound) {
		return pypiresolver.MaturinConfig{}, nil
	}
	if err != nil {
		return pypiresolver.MaturinConfig{}, errors.Wrapf(err, "looking up %s", pyprojPath)
	}
	pyProject, err := pypiresolver.ReadPyProject(pyf)
	if err != nil {
		// NOTE: Malformed syntax rejects this candidate rather than aborting inference.
		var decodeErr *toml.DecodeError
		if errors.As(err, &decodeErr) {
			return pypiresolver.MaturinConfig{}, nil
		}
		return pypiresolver.MaturinConfig{}, err
	}
	return pyProject.Tool.Maturin, nil
}

func maturinPyprojectFeatures(raw any) ([]string, bool) {
	if raw == nil {
		return nil, true
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	var features []string
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		trimmed := strings.TrimSpace(s)
		if trimmed == "" || !validMaturinFeaturePat.MatchString(trimmed) {
			return nil, false
		}
		features = append(features, trimmed)
	}
	return features, true
}

// findCargoManifest locates the Cargo.toml file for a Python package in tree at dir.
// It checks manifestPath (from [tool.maturin] manifest-path in pyproject.toml) first,
// then Cargo.toml in dir, and finally searches for a single Cargo.toml in the subtree under dir.
func findCargoManifest(tree *git2.Tree, dir, manifestPath string) (*git2.File, error) {
	if tree == nil {
		return nil, nil
	}
	if manifestPath != "" {
		fullPath := path.Join(dir, manifestPath)
		cf, err := tree.File(fullPath)
		if errors.Is(err, git2.ErrFileNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, errors.Wrapf(err, "looking up %s", fullPath)
		}
		return cf, nil
	}
	cargoPath := path.Join(dir, "Cargo.toml")
	cf, err := tree.File(cargoPath)
	if err == nil {
		return cf, nil
	}
	if !errors.Is(err, git2.ErrFileNotFound) {
		return nil, errors.Wrapf(err, "looking up %s", cargoPath)
	}
	var prefix string
	if cleanDir := path.Clean(dir); cleanDir != "." {
		prefix = cleanDir + "/"
	}
	var matches []*git2.File
	if err := tree.Files().ForEach(func(f *git2.File) error {
		if prefix != "" && !strings.HasPrefix(f.Name, prefix) {
			return nil
		}
		if path.Base(f.Name) == "Cargo.toml" {
			matches = append(matches, f)
		}
		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "iterating tree files")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return nil, nil
}

func targetPyO3VersionSuffix(pythonTag string) string {
	digits := strings.TrimPrefix(strings.TrimPrefix(pythonTag, "cp"), "py")
	if digits == "" {
		return ""
	}
	return "py" + digits
}

func isPyO3ABI3FeatureDep(dep, abiTag string) bool {
	_, ok := parsePyO3ABI3FeatureDep(dep, abiTag)
	return ok
}

func parsePyO3ABI3FeatureDep(dep, abiTag string) (string, bool) {
	rest, ok := strings.CutPrefix(dep, "pyo3/")
	if !ok {
		rest, ok = strings.CutPrefix(dep, "pyo3?/")
	}
	if !ok {
		return "", false
	}
	abiPrefix := "abi3"
	if strings.Contains(abiTag, "abi3t") {
		abiPrefix = "abi3t"
	}
	if rest == abiPrefix {
		return "", true
	}
	if suffix, ok := strings.CutPrefix(rest, abiPrefix+"-"); ok && suffix != "" {
		return suffix, true
	}
	return "", false
}

// inferRustVersion determines the Rust toolchain version for a Rust-backed wheel
// using the following candidate order:
//  1. The rustc version recorded in the wheel's ELF .comment section.
//  2. A rust-toolchain.toml or rust-toolchain file in the build directory or repository root.
//  3. The stable Rust release current at registryTime.
func inferRustVersion(zr *zip.Reader, tree *git2.Tree, dir string, registryTime time.Time) (string, error) {
	v, err := inspectWheelRustVersion(zr)
	if err != nil {
		return "", err
	}
	if v != "" {
		return v, nil
	}
	ch, err := readRustToolchainFile(tree, dir)
	if err != nil {
		return "", err
	}
	if ch != "" {
		resolved, ok := resolveStableChannel(ch, registryTime)
		if !ok {
			return "", errors.Errorf("rust-toolchain channel %q has no known release at %s", ch, registryTime.Format(time.RFC3339))
		}
		return resolved, nil
	}
	if !registryTime.IsZero() {
		return cratesio.RustVersionAt(registryTime)
	}
	return "", errors.New("no rust toolchain version found")
}

// inspectWheelRustVersion scans non-bundled ELF shared objects in a wheel archive
// for the rustc compiler version recorded in the ELF .comment section.
func inspectWheelRustVersion(zr *zip.Reader) (string, error) {
	if zr == nil {
		return "", nil
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > maxELFFileSize {
			continue
		}
		baseName := path.Base(f.Name)
		if !sharedLibNamePat.MatchString(strings.ToLower(baseName)) || isBundledLibPath(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", errors.Wrapf(err, "opening %s in wheel", f.Name)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", errors.Wrapf(err, "reading %s in wheel", f.Name)
		}
		ef, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			continue
		}
		sec := ef.Section(".comment")
		if sec == nil {
			continue
		}
		comment, err := sec.Data()
		if err != nil {
			continue
		}
		for _, entry := range bytes.Split(comment, []byte{0}) {
			s := strings.TrimSpace(string(entry))
			m := rustcVersionPat.FindStringSubmatch(s)
			if m == nil {
				continue
			}
			if m[2] != "" {
				// TODO: Map dated -nightly (<sha> <YYYY-MM-DD>) comments to rustup nightly-<date> channels.
				return "", errors.Errorf("wheel built with non-stable rustc %s%s", m[1], m[2])
			}
			return m[1], nil
		}
	}
	return "", nil
}

func readRustToolchainFile(tree *git2.Tree, dir string) (string, error) {
	if tree == nil {
		return "", nil
	}
	var dirs []string
	if dir != "" && dir != "." {
		dirs = append(dirs, dir)
	}
	dirs = append(dirs, "")
	for _, d := range dirs {
		for _, name := range []string{"rust-toolchain.toml", "rust-toolchain"} {
			p := path.Join(d, name)
			f, err := tree.File(p)
			if errors.Is(err, git2.ErrFileNotFound) {
				continue
			}
			if err != nil {
				return "", errors.Wrapf(err, "looking up %s", p)
			}
			content, err := f.Contents()
			if err != nil {
				return "", errors.Wrapf(err, "reading %s", p)
			}
			v, err := parseRustToolchainContent(content)
			if err != nil {
				return "", errors.Wrapf(err, "parsing %s", p)
			}
			if v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

func parseRustToolchainContent(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", nil
	}
	var ch string
	if strings.HasPrefix(trimmed, "[") || strings.Contains(trimmed, "=") {
		var tc struct {
			Toolchain struct {
				Channel string `toml:"channel"`
			} `toml:"toolchain"`
			Channel string `toml:"channel"`
		}
		if err := toml.Unmarshal([]byte(trimmed), &tc); err != nil {
			return "", nil
		}
		ch = strings.TrimSpace(tc.Toolchain.Channel)
		if ch == "" {
			ch = strings.TrimSpace(tc.Channel)
		}
	} else {
		firstLine, _, _ := strings.Cut(trimmed, "\n")
		ch = strings.TrimSpace(firstLine)
	}
	if ch == "" || stableChannelPat.MatchString(ch) {
		return "", nil
	}
	if m := toolchainChannelPat.FindStringSubmatch(ch); m != nil {
		return m[1], nil
	}
	// TODO: Support dated nightly-YYYY-MM-DD channels.
	return "", errors.Errorf("unsupported rust-toolchain channel %q", ch)
}

// resolveStableChannel returns a 3-component channel as-is, or resolves a
// 2-component channel (such as "1.96") to the latest known patch release
// published on or before registryTime.
func resolveStableChannel(channel string, registryTime time.Time) (string, bool) {
	if strings.Count(channel, ".") == 2 {
		return channel, true
	}
	var latestAtPublish string
	if !registryTime.IsZero() {
		v, err := cratesio.RustVersionAt(registryTime)
		if err != nil {
			return "", false
		}
		latestAtPublish = v
	}
	resolved := ""
	for patch := 0; ; patch++ {
		candidate := fmt.Sprintf("%s.%d", channel, patch)
		if latestAtPublish != "" && semver.Cmp(candidate, latestAtPublish) > 0 {
			break
		}
		if _, err := cratesio.HasMUSLBuild(candidate); err != nil {
			break
		}
		resolved = candidate
	}
	return resolved, resolved != ""
}

// rustHostForPlatform returns the rustup host target and installer digest for a
// wheel's platform tag.
func rustHostForPlatform(platformTag string) (rustHost, error) {
	tags, err := platform.ParsePlatformTags(platformTag)
	if err != nil {
		return rustHost{}, err
	}
	highest, err := platform.HighestLibcVersionTag(tags)
	if err != nil {
		return rustHost{}, err
	}
	if highest.Arch != "x86_64" {
		return rustHost{}, errors.Errorf("unsupported architecture %q for rust toolchain", highest.Arch)
	}
	switch highest.LibcImpl {
	case platform.Musl:
		return rustHostX86_64Musl, nil
	case platform.Glibc:
		return rustHostX86_64GNU, nil
	default:
		return rustHost{}, errors.Errorf("unsupported libc %q for rust toolchain", highest.LibcImpl)
	}
}
