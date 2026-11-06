// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

func elfWithComment(comment string) []byte {
	commentBytes := append([]byte(comment), 0)
	shstrtab := []byte("\x00.shstrtab\x00.comment\x00")
	buf := new(bytes.Buffer)
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
	binary.LittleEndian.PutUint16(hdr[60:62], 3)  // e_shnum
	binary.LittleEndian.PutUint16(hdr[62:64], 1)  // e_shstrndx
	buf.Write(hdr)
	// Section 0: SHT_NULL
	buf.Write(make([]byte, 64))
	shstrtabOffset := uint64(64 + 3*64)
	commentOffset := shstrtabOffset + uint64(len(shstrtab))
	// Section 1: .shstrtab
	shStr := make([]byte, 64)
	binary.LittleEndian.PutUint32(shStr[0:4], 1)
	binary.LittleEndian.PutUint32(shStr[4:8], 3) // SHT_STRTAB
	binary.LittleEndian.PutUint64(shStr[24:32], shstrtabOffset)
	binary.LittleEndian.PutUint64(shStr[32:40], uint64(len(shstrtab)))
	binary.LittleEndian.PutUint64(shStr[48:56], 1)
	buf.Write(shStr)
	// Section 2: .comment
	shComment := make([]byte, 64)
	binary.LittleEndian.PutUint32(shComment[0:4], 11)
	binary.LittleEndian.PutUint32(shComment[4:8], 1) // SHT_PROGBITS
	binary.LittleEndian.PutUint64(shComment[24:32], commentOffset)
	binary.LittleEndian.PutUint64(shComment[32:40], uint64(len(commentBytes)))
	binary.LittleEndian.PutUint64(shComment[48:56], 1)
	buf.Write(shComment)
	buf.Write(shstrtab)
	buf.Write(commentBytes)
	return buf.Bytes()
}

func TestInspectWheelRustVersion(t *testing.T) {
	tests := []struct {
		name    string
		entries []archive.ZipEntry
		want    string
		wantErr bool
	}{
		{
			name: "RustcInWheelExtension",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.cpython-312-x86_64-linux-musl.so"},
					Body:       elfWithComment("GCC: (Alpine 14.2.0)\x00Linker: LLD 20.1.8\x00rustc version 1.97.1 (8bab26f4f 2026-07-14)"),
				},
			},
			want: "1.97.1",
		},
		{
			name: "BundledLibInLibsDirIgnored",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg.libs/libfoo-12345678.so.1"},
					Body:       elfWithComment("rustc version 1.90.0\x00Linker: LLD 20.1.8"),
				},
			},
			want: "",
		},
		{
			name: "NightlyRustcRejected",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.cpython-312-x86_64-linux-gnu.so"},
					Body:       elfWithComment("rustc version 1.98.0-nightly (abc 2026-08-01)"),
				},
			},
			wantErr: true,
		},
		{
			name: "BetaRustcRejected",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.cpython-312-x86_64-linux-gnu.so"},
					Body:       elfWithComment("rustc version 1.98.0-beta.3 (abc 2026-08-01)"),
				},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := must(archivetest.ZipFile(tc.entries))
			zr := must(zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())))
			got, err := inspectWheelRustVersion(zr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("inspectWheelRustVersion() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("inspectWheelRustVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRustToolchainContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"PlainToolchainFile", "1.95.0\n", "1.95.0", false},
		{"PlainToolchainWithTargetTriple", "1.94.0-x86_64-unknown-linux-gnu\n", "1.94.0", false},
		{"TOMLWithChannelAndPatch", "[toolchain]\nchannel = \"1.96.0\"\n", "1.96.0", false},
		{"TOMLWithTwoComponentChannel", "[toolchain]\nchannel = \"1.96\"\n", "1.96", false},
		{"PlainStableChannelFallsThrough", "stable\n", "", false},
		{"PlainStableWithTargetTripleFallsThrough", "stable-x86_64-unknown-linux-gnu\n", "", false},
		{"TOMLStableChannelFallsThrough", "[toolchain]\nchannel = \"stable\"\n", "", false},
		{"TOMLNightlyChannelRejected", "[toolchain]\nchannel = \"nightly-2026-01-01\"\n", "", true},
		{"TOMLBetaVersionChannelRejected", "[toolchain]\nchannel = \"1.98.0-beta.1\"\n", "", true},
		{"Empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRustToolchainContent(tc.content)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseRustToolchainContent(%q) error = %v, wantErr %v", tc.content, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseRustToolchainContent(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestUsesRust(t *testing.T) {
	tests := []struct {
		name string
		reqs []string
		repo string
		dir  string
		want bool
	}{
		{
			name: "MaturinRequirement",
			reqs: []string{"maturin==1.13.1"},
			want: true,
		},
		{
			name: "SetuptoolsRustRequirement",
			reqs: []string{"setuptools>=61", "setuptools-rust>=1.9.0"},
			want: true,
		},
		{
			name: "SetuptoolsRustInSetupPy",
			reqs: []string{"setuptools>=61"},
			repo: `commits:
  - id: initial
    files:
      sub/setup.py: |
        from setuptools import setup
        from setuptools_rust import RustExtension
        setup(name="pkg", rust_extensions=[RustExtension("pkg._rust")])
`,
			dir:  "sub",
			want: true,
		},
		{
			name: "PlainSetuptoolsWithoutRust",
			reqs: []string{"setuptools>=61"},
			repo: `commits:
  - id: initial
    files:
      setup.py: |
        from setuptools import setup
        setup(name="pkg")
      Cargo.toml: |
        [package]
        name = "unused"
        version = "0.1.0"
`,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tree *git2.Tree
			if tc.repo != "" {
				r := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
				defer r.Close()
				commit := must(r.Repository.CommitObject(git2.Hash(r.Commits["initial"])))
				tree = must(commit.Tree())
			}
			got, err := usesRust(tc.reqs, tree, tc.dir)
			if err != nil {
				t.Fatalf("usesRust() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("usesRust() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInferRustVersion(t *testing.T) {
	publishTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		entries      []archive.ZipEntry
		repo         string
		dir          string
		registryTime time.Time
		want         string
		wantErr      bool
	}{
		{
			name: "ELFCommentPrecedenceOverToolchainAndDate",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.cpython-312-x86_64-linux-gnu.so"},
					Body:       elfWithComment("rustc version 1.95.0 (abc 2026-04-16)"),
				},
			},
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "1.94.0"
`,
			registryTime: publishTime,
			want:         "1.95.0",
		},
		{
			name: "DirToolchainFilePrecedenceOverRepoRootAndDate",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "1.93.0"
      py/rust-toolchain: |
        1.94.1
`,
			dir:          "py",
			registryTime: publishTime,
			want:         "1.94.1",
		},
		{
			name: "TwoComponentChannelResolvedAtRegistryTime",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "1.96"
`,
			registryTime: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			want:         "1.96.0",
		},
		{
			name: "TwoComponentChannelResolvesLatestPatchBeforeRegistryTime",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "1.96"
`,
			registryTime: publishTime,
			want:         "1.96.1",
		},
		{
			name: "StableChannelFallsThroughToRegistryTime",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "stable"
`,
			registryTime: publishTime,
			want:         "1.96.1",
		},
		{
			name: "PinnedChannelNewerThanRegistryTimeReturnsError",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "1.96"
`,
			registryTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			wantErr:      true,
		},
		{
			name: "NightlyToolchainChannelReturnsError",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "nightly-2026-01-01"
`,
			registryTime: publishTime,
			wantErr:      true,
		},
		{
			name:    "NoCandidateWithZeroTimeReturnsError",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var zr *zip.Reader
			if len(tc.entries) > 0 {
				buf := must(archivetest.ZipFile(tc.entries))
				zr = must(zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())))
			}
			var tree *git2.Tree
			if tc.repo != "" {
				r := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
				defer r.Close()
				commit := must(r.Repository.CommitObject(git2.Hash(r.Commits["initial"])))
				tree = must(commit.Tree())
			}
			got, err := inferRustVersion(zr, tree, tc.dir, tc.registryTime)
			if (err != nil) != tc.wantErr {
				t.Fatalf("inferRustVersion() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("inferRustVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRustHostForPlatform(t *testing.T) {
	tests := []struct {
		name        string
		platformTag string
		want        rustHost
		wantErr     bool
	}{
		{"Manylinux2014", "manylinux2014_x86_64", rustHostX86_64GNU, false},
		{"Musllinux", "musllinux_1_2_x86_64", rustHostX86_64Musl, false},
		{"CompressedManylinux", "manylinux_2_17_x86_64.manylinux2014_x86_64", rustHostX86_64GNU, false},
		{"UnsupportedArchAarch64", "manylinux2014_aarch64", rustHost{}, true},
		{"Empty", "", rustHost{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rustHostForPlatform(tc.platformTag)
			if (err != nil) != tc.wantErr {
				t.Fatalf("rustHostForPlatform(%q) error = %v, wantErr %v", tc.platformTag, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("rustHostForPlatform(%q) = %+v, want %+v", tc.platformTag, got, tc.want)
			}
		})
	}
}

func TestInferStrategyRustPlatformWheel(t *testing.T) {
	const filename = "rustpkg-1.0.0-cp312-cp312-musllinux_1_2_x86_64.whl"
	const wheelURL = "https://files.pythonhosted.org/" + filename
	wheelBuf := must(archivetest.ZipFile([]archive.ZipEntry{
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/WHEEL"},
			Body:       []byte("Wheel-Version: 1.0\nGenerator: setuptools (70.0.0)\nRoot-Is-Purelib: false\nTag: cp312-cp312-musllinux_1_2_x86_64\n"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/METADATA"},
			Body:       []byte("Metadata-Version: 2.1\nName: rustpkg\nVersion: 1.0.0\n"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg/_native.cpython-312-x86_64-linux-musl.so"},
			Body:       elfWithComment("Linker: LLD 20.1.8\x00rustc version 1.97.1 (8bab26f4f 2026-07-14)"),
		},
	}))
	releaseJSON := fmt.Sprintf(`{"info":{"name":"rustpkg","version":"1.0.0"},"urls":[{"filename":%q,"url":%q,"size":%d,"upload_time_iso_8601":"2026-08-01T00:00:00Z"}]}`, filename, wheelURL, wheelBuf.Len())
	tests := []struct {
		name      string
		beforeAll string
		wantDeps  []string
	}{
		{
			name:      "PinnedLLDPreserved",
			beforeAll: "apk add lld=20.1.8-r0",
			wantDeps:  []string{"lld=20.1.8-r0"},
		},
		{
			name:      "RangePinnedLLDPreserved",
			beforeAll: "apk add 'lld>=20.1'",
			wantDeps:  []string{"lld>=20.1"},
		},
		{
			name:      "DistinctPackagePrefixStillAddsLLD",
			beforeAll: "apk add lld-dev=20.1.8-r0",
			wantDeps:  []string{"lld-dev=20.1.8-r0", "lld"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pyproject := fmt.Sprintf("[project]\nname = \"rustpkg\"\nversion = \"1.0.0\"\n[build-system]\nrequires = [\"setuptools>=61\", \"setuptools-rust>=1.9.0\"]\n[tool.cibuildwheel.linux]\nbefore-all = %q\n", tc.beforeAll)
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{
				ID:    "release",
				Tag:   "v1.0.0",
				Files: gitxtest.FileContent{"pyproject.toml": pyproject},
			}}, nil))
			defer repo.Close()
			mux := rebuild.RegistryMux{PyPI: pypireg.HTTPRegistry{Client: &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{URL: "https://pypi.org/pypi/rustpkg/1.0.0/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(releaseJSON)}},
					{URL: "https://pypi.org/pypi/rustpkg/1.0.0/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(releaseJSON)}},
					{URL: wheelURL, Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wheelBuf.Bytes()))}},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			}}}
			rcfg := &rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository, Storer: repo.Storer}, URI: "https://github.com/example/rustpkg"}
			target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "rustpkg", Version: "1.0.0", Artifact: filename}
			strat, err := Rebuilder{}.InferStrategy(context.Background(), target, mux, rcfg, nil)
			if err != nil {
				t.Fatalf("InferStrategy() unexpected error: %v", err)
			}
			got, ok := strat.(*PlatformWheelBuild)
			if !ok {
				t.Fatalf("InferStrategy() returned %T, want *PlatformWheelBuild", strat)
			}
			if got.RustVersion != "1.97.1" {
				t.Errorf("PlatformWheelBuild.RustVersion = %q, want %q", got.RustVersion, "1.97.1")
			}
			if diff := cmp.Diff(tc.wantDeps, got.SystemDeps); diff != "" {
				t.Errorf("PlatformWheelBuild.SystemDeps diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInferStrategyUninferableRustToolchain(t *testing.T) {
	const filename = "rustpkg-1.0.0-cp312-cp312-manylinux_2_28_x86_64.whl"
	const wheelURL = "https://files.pythonhosted.org/" + filename
	wheelBuf := must(archivetest.ZipFile([]archive.ZipEntry{
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/WHEEL"},
			Body:       []byte("Wheel-Version: 1.0\nGenerator: setuptools (70.0.0)\nRoot-Is-Purelib: false\nTag: cp312-cp312-manylinux_2_28_x86_64\n"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/METADATA"},
			Body:       []byte("Metadata-Version: 2.1\nName: rustpkg\nVersion: 1.0.0\n"),
		},
		{
			FileHeader: &zip.FileHeader{Name: "rustpkg/_native.cpython-312-x86_64-linux-gnu.so"},
			Body:       elfWithComment("GCC: (GNU) 13.2.1"),
		},
	}))
	releaseJSON := fmt.Sprintf(`{"info":{"name":"rustpkg","version":"1.0.0"},"urls":[{"filename":%q,"url":%q,"size":%d}]}`, filename, wheelURL, wheelBuf.Len())
	repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{
		ID:  "release",
		Tag: "v1.0.0",
		Files: gitxtest.FileContent{
			"pyproject.toml": "[project]\nname = \"rustpkg\"\nversion = \"1.0.0\"\n[build-system]\nrequires = [\"setuptools>=61\", \"setuptools-rust>=1.9.0\"]\n",
		},
	}}, nil))
	defer repo.Close()
	ref := repo.Commits["release"].String()
	mux := rebuild.RegistryMux{PyPI: pypireg.HTTPRegistry{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{URL: "https://pypi.org/pypi/rustpkg/1.0.0/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(releaseJSON)}},
			{URL: "https://pypi.org/pypi/rustpkg/1.0.0/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(releaseJSON)}},
			{URL: wheelURL, Response: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wheelBuf.Bytes()))}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}}}
	rcfg := &rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository, Storer: repo.Storer}, URI: "https://github.com/example/rustpkg"}
	target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "rustpkg", Version: "1.0.0", Artifact: filename}
	strat, err := Rebuilder{}.InferStrategy(context.Background(), target, mux, rcfg, nil)
	if err == nil {
		t.Fatalf("InferStrategy() = %v, want error", strat)
	}
	var located *rebuild.InferenceError
	if !errors.As(err, &located) {
		t.Fatalf("InferStrategy() error = %v, want *rebuild.InferenceError", err)
	}
	want := rebuild.InferenceErrorDetail{Location: rebuild.Location{Repo: rcfg.URI, Ref: ref}}
	if diff := cmp.Diff(want, located.Detail); diff != "" {
		t.Errorf("InferenceError.Detail diff (-want +got):\n%s", diff)
	}
}
