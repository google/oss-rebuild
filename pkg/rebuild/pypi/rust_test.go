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
			name: "NightlyRustcAdvancesDateByOneDay",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.abi3.so"},
					Body:       elfWithComment("GCC: (GNU) 10.2.1\x00rustc version 1.97.0-nightly (f53b654a8 2026-04-30)"),
				},
			},
			want: "nightly-2026-05-01",
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
			name: "DatelessNightlyRustcRejected",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.abi3.so"},
					Body:       elfWithComment("rustc version 1.97.0-nightly"),
				},
			},
			wantErr: true,
		},
		{
			name: "InvalidDateNightlyRustcRejected",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.cpython-312-x86_64-linux-gnu.so"},
					Body:       elfWithComment("rustc version 1.98.0-nightly (abc 2026-02-30)"),
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
		{
			name: "DevRustcRejected",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.abi3.so"},
					Body:       elfWithComment("rustc version 1.97.0-dev"),
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
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("inspectWheelRustVersion() diff (-want +got):\n%s", diff)
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
		{"TOMLDatedNightlyChannel", "[toolchain]\nchannel = \"nightly-2026-09-01\"\n", "nightly-2026-09-01", false},
		{"PlainDatedNightlyWithTargetTriple", "nightly-2026-09-01-x86_64-unknown-linux-gnu\n", "nightly-2026-09-01", false},
		{"PlainStableChannelFallsThrough", "stable\n", "", false},
		{"PlainStableWithTargetTripleFallsThrough", "stable-x86_64-unknown-linux-gnu\n", "", false},
		{"TOMLStableChannelFallsThrough", "[toolchain]\nchannel = \"stable\"\n", "", false},
		{"PlainBareNightlyChannelRejected", "nightly\n", "", true},
		{"TOMLBareNightlyChannelRejected", "[toolchain]\nchannel = \"nightly\"\n", "", true},
		{"TOMLNightlyInvalidDateRejected", "[toolchain]\nchannel = \"nightly-2026-02-30\"\n", "", true},
		{"TOMLBetaVersionChannelRejected", "[toolchain]\nchannel = \"1.98.0-beta.1\"\n", "", true},
		{"Empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRustToolchainContent(tc.content)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseRustToolchainContent(%q) error = %v, wantErr %v", tc.content, err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseRustToolchainContent(%q) diff (-want +got):\n%s", tc.content, diff)
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
			name: "DatedNightlyToolchainChannel",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "nightly-2026-05-01"
`,
			registryTime: publishTime,
			want:         "nightly-2026-05-01",
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
			name: "BareNightlyToolchainChannelReturnsError",
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "nightly"
`,
			registryTime: publishTime,
			wantErr:      true,
		},
		{
			name: "BetaCommentReturnsErrorEvenWithToolchainFile",
			entries: []archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "pkg/_native.abi3.so"},
					Body:       elfWithComment("rustc version 1.97.0-beta.1 (abc 2026-04-30)"),
				},
			},
			repo: `commits:
  - id: initial
    files:
      rust-toolchain.toml: |
        [toolchain]
        channel = "nightly-2026-05-01"
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

func TestInferMaturinFeatures(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		dir       string
		pythonTag string
		abiTag    string
		want      []string
	}{
		{
			name: "NonDefaultABI3FeatureInSubcrateViaPyprojectManifestPath",
			repo: `commits:
  - id: initial
    files:
      hypothesis-python/pyproject.toml: |
        [tool.maturin]
        manifest-path = "../conjecture-rust/Cargo.toml"
      conjecture-rust/Cargo.toml: |
        [package]
        name = "conjecture_rust"
        version = "0.1.0"
        [features]
        default = ["extension-module"]
        extension-module = ["pyo3/extension-module"]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "hypothesis-python",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      []string{"abi3"},
		},
		{
			name: "ABI3AlreadyInDefaultFeatures",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "datefinder"
        version = "1.0.0"
        [features]
        default = ["abi3"]
        abi3 = ["pyo3/abi3-py39"]
`,
			dir:       "",
			pythonTag: "cp39",
			abiTag:    "abi3",
			want:      nil,
		},
		{
			name: "NonABI3WheelSkipsABI3Feature",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "conjecture_rust"
        version = "0.1.0"
        [features]
        default = ["extension-module"]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp312",
			abiTag:    "cp312",
			want:      nil,
		},
		{
			name: "ABI3tWheelSelectsABI3tFeature",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "conjecture_rust"
        version = "0.1.0"
        [features]
        default = ["extension-module"]
        abi3 = ["pyo3/abi3-py310"]
        abi3t = ["pyo3/abi3t-py315"]
`,
			dir:       "",
			pythonTag: "cp315",
			abiTag:    "abi3.abi3t",
			want:      []string{"abi3t"},
		},
		{
			name: "MultiplePerVersionFeaturesSelectsMatchingPythonTag",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        default = ["extension-module"]
        abi3-py38 = ["pyo3/abi3-py38"]
        abi3-py310 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      []string{"abi3-py310"},
		},
		{
			name: "MultiplePerVersionFeaturesWithoutTargetMatchReturnsNil",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        default = ["extension-module"]
        abi3-py38 = ["pyo3/abi3-py38"]
        abi3-py310 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp312",
			abiTag:    "abi3",
			want:      nil,
		},
		{
			name: "WeakDependencySyntaxMatched",
			repo: `commits:
  - id: initial
    files:
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        default = []
        abi3 = ["pyo3?/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      []string{"abi3"},
		},
		{
			name: "SingleNestedCargoManifestFallback",
			repo: `commits:
  - id: initial
    files:
      crates/core/Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        default = []
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      []string{"abi3"},
		},
		{
			name: "AmbiguousMultipleNestedCargoManifestsReturnsNil",
			repo: `commits:
  - id: initial
    files:
      crates/a/Cargo.toml: |
        [package]
        name = "a"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
      crates/b/Cargo.toml: |
        [package]
        name = "b"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      nil,
		},
		{
			name: "MissingExplicitManifestPathDoesNotFallThrough",
			repo: `commits:
  - id: initial
    files:
      pyproject.toml: |
        [tool.maturin]
        manifest-path = "missing/Cargo.toml"
      Cargo.toml: |
        [package]
        name = "root"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      nil,
		},
		{
			name: "PyprojectMaturinFeaturesPreservedWithInferredABI3",
			repo: `commits:
  - id: initial
    files:
      pyproject.toml: |
        [tool.maturin]
        features = ["pyo3/extension-module"]
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      []string{"pyo3/extension-module", "abi3"},
		},
		{
			name: "ABI3AlreadyInPyprojectMaturinFeatures",
			repo: `commits:
  - id: initial
    files:
      pyproject.toml: |
        [tool.maturin]
        features = ["pyo3/extension-module", "pyo3/abi3-py38"]
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      nil,
		},
		{
			name: "ConditionalPyprojectMaturinFeaturesRejected",
			repo: `commits:
  - id: initial
    files:
      pyproject.toml: |
        [tool.maturin]
        features = [{ feature = "pyo3/extension-module", python-version = ">=3.10" }]
      Cargo.toml: |
        [package]
        name = "pkg"
        version = "0.1.0"
        [features]
        abi3 = ["pyo3/abi3-py310"]
`,
			dir:       "",
			pythonTag: "cp310",
			abiTag:    "abi3",
			want:      nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
			defer r.Close()
			commit := must(r.Repository.CommitObject(git2.Hash(r.Commits["initial"])))
			tree := must(commit.Tree())
			got, err := inferMaturinFeatures(tree, tc.dir, WheelTags{Python: tc.pythonTag, ABI: tc.abiTag})
			if err != nil {
				t.Fatalf("inferMaturinFeatures() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("inferMaturinFeatures() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInferStrategyMaturinWheel(t *testing.T) {
	cargoWithABI3 := "[package]\nname = \"rustpkg\"\nversion = \"1.0.0\"\n[features]\ndefault = [\"extension-module\"]\nextension-module = [\"pyo3/extension-module\"]\nabi3 = [\"pyo3/abi3-py310\"]\n"
	basePyproject := "[project]\nname = \"rustpkg\"\nversion = \"1.0.0\"\n[build-system]\nrequires = [\"maturin>=1.0,<2.0\"]\nbuild-backend = \"maturin\"\n"
	tests := []struct {
		name        string
		filename    string
		wheelTag    string
		files       gitxtest.FileContent
		wantMaturin *MaturinOptions
	}{
		{
			name:     "DirectMaturinABI3SelectsFeature",
			filename: "rustpkg-1.0.0-cp310-abi3-manylinux_2_28_x86_64.whl",
			wheelTag: "cp310-abi3-manylinux_2_28_x86_64",
			files: gitxtest.FileContent{
				"pyproject.toml": basePyproject,
				"Cargo.toml":     cargoWithABI3,
			},
			wantMaturin: &MaturinOptions{Features: []string{"abi3"}},
		},
		{
			name:     "DirectMaturinNonABI3KeepsFeaturesEmpty",
			filename: "rustpkg-1.0.0-cp312-cp312-manylinux_2_28_x86_64.whl",
			wheelTag: "cp312-cp312-manylinux_2_28_x86_64",
			files: gitxtest.FileContent{
				"pyproject.toml": basePyproject,
				"Cargo.toml":     cargoWithABI3,
			},
			wantMaturin: &MaturinOptions{},
		},
		{
			name:     "PyprojectCibuildwheelUsesPlatformWheel",
			filename: "rustpkg-1.0.0-cp310-abi3-manylinux_2_28_x86_64.whl",
			wheelTag: "cp310-abi3-manylinux_2_28_x86_64",
			files: gitxtest.FileContent{
				"pyproject.toml": basePyproject + "[tool.cibuildwheel]\nbuild = \"cp310-*\"\n",
				"Cargo.toml":     cargoWithABI3,
			},
			wantMaturin: nil,
		},
		{
			name:     "UnpinnedWorkflowCibuildwheelUsesPlatformWheel",
			filename: "rustpkg-1.0.0-cp310-abi3-manylinux_2_28_x86_64.whl",
			wheelTag: "cp310-abi3-manylinux_2_28_x86_64",
			files: gitxtest.FileContent{
				"pyproject.toml":               basePyproject,
				"Cargo.toml":                   cargoWithABI3,
				".github/workflows/wheels.yml": "jobs:\n  wheels:\n    steps:\n      - uses: pypa/cibuildwheel@main\n",
			},
			wantMaturin: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wheelURL := "https://files.pythonhosted.org/" + tc.filename
			wheelBuf := must(archivetest.ZipFile([]archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/WHEEL"},
					Body:       []byte("Wheel-Version: 1.0\nGenerator: maturin (1.13.1)\nRoot-Is-Purelib: false\nTag: " + tc.wheelTag + "\n"),
				},
				{
					FileHeader: &zip.FileHeader{Name: "rustpkg-1.0.0.dist-info/METADATA"},
					Body:       []byte("Metadata-Version: 2.1\nName: rustpkg\nVersion: 1.0.0\n"),
				},
				{
					FileHeader: &zip.FileHeader{Name: "rustpkg/_native.abi3.so"},
					Body:       elfWithComment("rustc version 1.97.1 (8bab26f4f 2026-07-14)"),
				},
			}))
			releaseJSON := fmt.Sprintf(`{"info":{"name":"rustpkg","version":"1.0.0"},"urls":[{"filename":%q,"url":%q,"size":%d,"upload_time_iso_8601":"2026-08-01T00:00:00Z"}]}`, tc.filename, wheelURL, wheelBuf.Len())
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{
				ID:    "release",
				Tag:   "v1.0.0",
				Files: tc.files,
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
			target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "rustpkg", Version: "1.0.0", Artifact: tc.filename}
			strat, err := Rebuilder{}.InferStrategy(context.Background(), target, mux, rcfg, nil)
			if err != nil {
				t.Fatalf("InferStrategy() unexpected error: %v", err)
			}
			got, ok := strat.(*PlatformWheelBuild)
			if !ok {
				t.Fatalf("InferStrategy() returned %T, want *PlatformWheelBuild", strat)
			}
			if diff := cmp.Diff(tc.wantMaturin, got.Maturin); diff != "" {
				t.Errorf("PlatformWheelBuild.Maturin diff (-want +got):\n%s", diff)
			}
		})
	}
}
