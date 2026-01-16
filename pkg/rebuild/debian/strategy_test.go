// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDebianPackage(t *testing.T) {
	tests := []struct {
		name     string
		strategy *DebianPackage
		target   rebuild.Target
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "StandardPackage",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.dsc",
					MD5: "abc123",
				},
				Orig: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.orig.tar.gz",
					MD5: "def456",
				},
				Debian: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.debian.tar.xz",
					MD5: "ghi789",
				},
				Requirements: []string{"build-dep1", "build-dep2"},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0-1",
				Artifact:  "pkg_1.0-1_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `wget https://example.com/pkg_1.0-1.dsc
wget https://example.com/pkg_1.0.orig.tar.gz
wget https://example.com/pkg_1.0-1.debian.tar.xz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0-1.dsc")`,
				Deps: `apt update
apt install -y build-dep1 build-dep2`,
				Build: `cd */
debuild -b -uc -us`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				},
				OutputPath: "pkg_1.0-1_amd64.deb",
			},
		},
		{
			name: "NativePackage",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.dsc",
					MD5: "abc123",
				},
				Native: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.tar.gz",
					MD5: "def456",
				},
				Requirements: []string{"build-dep1"},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0",
				Artifact:  "pkg_1.0_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `wget https://example.com/pkg_1.0.dsc
wget https://example.com/pkg_1.0.tar.gz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0.dsc")`,
				Deps: `apt update
apt install -y build-dep1`,
				Build: `cd */
debuild -b -uc -us`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				},
				OutputPath: "pkg_1.0_amd64.deb",
			},
		},
		{
			name: "BinaryOnlyRebuild",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.dsc",
					MD5: "abc123",
				},
				Orig: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.orig.tar.gz",
					MD5: "def456",
				},
				Debian: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.debian.tar.xz",
					MD5: "ghi789",
				},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0-1+b1",
				Artifact:  "pkg_1.0-1+b1_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `wget https://example.com/pkg_1.0-1.dsc
wget https://example.com/pkg_1.0.orig.tar.gz
wget https://example.com/pkg_1.0-1.debian.tar.xz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0-1.dsc")`,
				Deps: `apt update
apt install -y`,
				Build: `cd */
debuild -b -uc -us
mv /src/pkg_1.0-1_amd64.deb /src/pkg_1.0-1+b1_amd64.deb`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				},
				OutputPath: "pkg_1.0-1+b1_amd64.deb",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.strategy.GenerateFor(tc.target, tc.env)
			if err != nil {
				t.Fatalf("DebianPackage.GenerateFor() failed unexpectedly: %v", err)
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("DebianPackage.GenerateFor() returned diff (-got +want):\n%s", diff)
			}
		})
	}
}
func TestDebrebuild(t *testing.T) {
	tests := []struct {
		name     string
		strategy Debrebuild
		target   rebuild.Target
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "NormalRelease",
			strategy: Debrebuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/a/acl/acl_2.3.2-2_amd64.buildinfo",
					MD5: "deadbeef",
				},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/acl",
				Version:   "2.3.1-3",
				Artifact:  "acl_2.3.1-3_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `wget https://buildinfos.debian.net/buildinfo-pool/a/acl/acl_2.3.2-2_amd64.buildinfo`,
				Deps: `apt -o Acquire::Check-Valid-Until=false update
apt install -y devscripts mmdebstrap sbuild`,
				Build: `echo "root:100000:65536" > /etc/subuid
echo "root:100000:65536" > /etc/subgid
debrebuild --buildresult=./out --builder=sbuild+unshare acl_2.3.2-2_amd64.buildinfo`,
				OutputPath: "out/acl_2.3.1-3_amd64.deb",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget"},
					Privileged: true,
				},
			},
		},
		{
			name: "NoCheck",
			strategy: Debrebuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/a/acl/acl_2.3.2-2_amd64.buildinfo",
					MD5: "deadbeef",
				},
				UseNoCheck: true,
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/acl",
				Version:   "2.3.1-3",
				Artifact:  "acl_2.3.1-3_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `wget https://buildinfos.debian.net/buildinfo-pool/a/acl/acl_2.3.2-2_amd64.buildinfo
BUILDINFO_FILE='acl_2.3.2-2_amd64.buildinfo'
NEW_PROFILE_ENTRY='DEB_BUILD_PROFILES="nocheck"'
if grep -q "DEB_BUILD_PROFILES=" "$BUILDINFO_FILE"; then
    sed -i '/^\(Environment:\|\s\+\)DEB_BUILD_PROFILES=/s/DEB_BUILD_PROFILES=.*$/'"$NEW_PROFILE_ENTRY"'/' "$BUILDINFO_FILE"
else
    sed -i '/^Environment:/a \ '"$NEW_PROFILE_ENTRY"'' "$BUILDINFO_FILE"
fi`,
				Deps: `apt -o Acquire::Check-Valid-Until=false update
apt install -y devscripts mmdebstrap sbuild`,
				Build: `echo "root:100000:65536" > /etc/subuid
echo "root:100000:65536" > /etc/subgid
debrebuild --buildresult=./out --builder=sbuild+unshare acl_2.3.2-2_amd64.buildinfo`,
				OutputPath: "out/acl_2.3.1-3_amd64.deb",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget"},
					Privileged: true,
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.strategy.GenerateFor(tc.target, tc.env)
			if err != nil {
				t.Fatalf("Debrebuild.GenerateFor() failed unexpectedly: %v", err)
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("Debrebuild.GenerateFor() returned diff (-got +want):\n%s", diff)
			}
		})
	}
}

func TestBinaryVersionRegex(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantMap map[string]string
	}{
		{
			name:  "StandardBinaryPackage",
			input: "pkg_1.0-1+b1_amd64.deb",
			want:  true,
			wantMap: map[string]string{
				"name":              "pkg",
				"nonbinary_version": "1.0-1",
				"arch":              "amd64",
			},
		},
		{
			name:    "NonBinaryPackage",
			input:   "pkg_1.0-1_amd64.deb",
			want:    false,
			wantMap: map[string]string{},
		},
		{
			name:    "InvalidFormat",
			input:   "invalid-package-name",
			want:    false,
			wantMap: map[string]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := binaryVersionRegex.FindStringSubmatch(tc.input)
			got := matches != nil
			if got != tc.want {
				t.Errorf("binaryVersionRegex.FindStringSubmatch(%q) = %v, want %v", tc.input, got, tc.want)
			}

			if got {
				gotMap := make(map[string]string)
				for i, name := range binaryVersionRegex.SubexpNames() {
					if i != 0 && name != "" {
						gotMap[name] = matches[i]
					}
				}
				if diff := cmp.Diff(gotMap, tc.wantMap); diff != "" {
					t.Errorf("binaryVersionRegex capture groups returned diff (-got +want):\n%s", diff)
				}
			}
		})
	}
}

func TestUpstreamSourceArchive(t *testing.T) {
	tests := []struct {
		name     string
		strategy *UpstreamSourceArchive
		target   rebuild.Target
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "XZCompression",
			strategy: &UpstreamSourceArchive{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo.git",
					Ref:  "v1.2.3",
				},
				Compression:    "xz",
				Prefix:         "example-1.2.3/",
				OutputFilename: "example_1.2.3.orig.tar.xz",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/example",
				Version:   "1.2.3-1",
				Artifact:  "example_1.2.3.orig.tar.xz",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo.git",
					Ref:  "v1.2.3",
				},
				Source: `git clone https://github.com/example/repo.git .
git checkout --force 'v1.2.3'`,
				Build: `git archive --format=tar --prefix=example-1.2.3/ 'v1.2.3' | xz -c > "example_1.2.3.orig.tar.xz"`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "xz-utils", "gzip", "bzip2"},
				},
				OutputPath: "example_1.2.3.orig.tar.xz",
			},
		},
		{
			name: "GzipCompression",
			strategy: &UpstreamSourceArchive{
				Location: rebuild.Location{
					Repo: "https://gitlab.com/example/repo.git",
					Ref:  "1.0.0",
				},
				Compression:    "gz",
				Prefix:         "example-1.0.0/",
				OutputFilename: "example_1.0.0.orig.tar.gz",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/example",
				Version:   "1.0.0-1",
				Artifact:  "example_1.0.0.orig.tar.gz",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://gitlab.com/example/repo.git",
					Ref:  "1.0.0",
				},
				Source: `git clone https://gitlab.com/example/repo.git .
git checkout --force '1.0.0'`,
				Build: `git archive --format=tar --prefix=example-1.0.0/ '1.0.0' | gzip -c > "example_1.0.0.orig.tar.gz"`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "xz-utils", "gzip", "bzip2"},
				},
				OutputPath: "example_1.0.0.orig.tar.gz",
			},
		},
		{
			name: "WithSubdirectory",
			strategy: &UpstreamSourceArchive{
				Location: rebuild.Location{
					Repo: "https://github.com/monorepo/project.git",
					Ref:  "v2.0.0",
					Dir:  "packages/subproject",
				},
				Compression:    "xz",
				Prefix:         "subproject-2.0.0/",
				OutputFilename: "subproject_2.0.0.orig.tar.xz",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/subproject",
				Version:   "2.0.0-1",
				Artifact:  "subproject_2.0.0.orig.tar.xz",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/monorepo/project.git",
					Ref:  "v2.0.0",
					Dir:  "packages/subproject",
				},
				Source: `git clone https://github.com/monorepo/project.git .
git checkout --force 'v2.0.0'`,
				Build: `git archive --format=tar --prefix=subproject-2.0.0/ 'v2.0.0' -- 'packages/subproject' | xz -c > "subproject_2.0.0.orig.tar.xz"`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "xz-utils", "gzip", "bzip2"},
				},
				OutputPath: "subproject_2.0.0.orig.tar.xz",
			},
		},
		{
			name: "Bzip2Compression",
			strategy: &UpstreamSourceArchive{
				Location: rebuild.Location{
					Repo: "https://github.com/example/app.git",
					Ref:  "release-3.1.4",
				},
				Compression:    "bz2",
				Prefix:         "app-3.1.4/",
				OutputFilename: "app_3.1.4.orig.tar.bz2",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/app",
				Version:   "3.1.4-1",
				Artifact:  "app_3.1.4.orig.tar.bz2",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/example/app.git",
					Ref:  "release-3.1.4",
				},
				Source: `git clone https://github.com/example/app.git .
git checkout --force 'release-3.1.4'`,
				Build: `git archive --format=tar --prefix=app-3.1.4/ 'release-3.1.4' | bzip2 -c > "app_3.1.4.orig.tar.bz2"`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "xz-utils", "gzip", "bzip2"},
				},
				OutputPath: "app_3.1.4.orig.tar.bz2",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.strategy.GenerateFor(tc.target, tc.env)
			if err != nil {
				t.Fatalf("UpstreamOrig.GenerateFor() failed unexpectedly: %v", err)
			}

			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("UpstreamOrig.GenerateFor() returned diff (-got +want):\n%s", diff)
			}
		})
	}
}
