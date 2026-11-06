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
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/semver"
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
