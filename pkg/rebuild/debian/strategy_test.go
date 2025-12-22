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

func TestDebootsnapSbuild(t *testing.T) {
	tests := []struct {
		name     string
		strategy DebootsnapSbuild
		target   rebuild.Target
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "FullOptions",
			strategy: DebootsnapSbuild{
				BuildInfo: FileWithChecksum{
					URL: "http://example.com/pkg.buildinfo",
					MD5: "md5",
				},
				BuildArchAll:             true,
				BuildArchAny:             false,
				BuildArch:                "amd64",
				HostArch:                 "amd64",
				SrcPackage:               "pkg",
				SrcVersion:               "1.0",
				SrcVersionNoEpoch:        "1.0",
				BuildPath:                "/build/path",
				DscDir:                   "/dsc/dir",
				ForceRulesRequiresRootNo: true,
				Env:                      []string{"VAR=val"},
				BinaryOnlyChanges:        "changes",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0",
				Artifact:  "pkg_1.0_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `apt update
apt install -y devscripts mmdebstrap sbuild
wget http://example.com/pkg.buildinfo
debsnap --force --verbose --destdir ./ pkg 1.0`,
				Deps: `debootsnap --buildinfo="pkg.buildinfo" "/tmp/chroot.tar"`,
				Build: `if [ ! -e /dev/console ]; then
	mknod -m 666 /dev/console c 5 1
fi
cat <<"EOFSTEP" > "/tmp/sbuild.config"
$apt_get = '/bin/true';
$apt_cache = '/bin/true';
$build_as_root_when_needed = 1;
EOFSTEP
echo "root:100000:65536" > /etc/subuid
echo "root:100000:65536" > /etc/subgid
env --chdir=/src VAR=val SBUILD_CONFIG=/tmp/sbuild.config\
    sbuild \
    --build=amd64 \
    --host=amd64 \
    --no-arch-any \
    --arch-all \
    --binNMU-changelog="changes" \
    --chroot="/tmp/chroot.tar" \
    --chroot-mode=unshare \
    --dist=unstable \
    --no-run-lintian \
    --no-run-piuparts \
    --no-run-autopkgtest \
    --no-apt-update \
    --no-apt-upgrade \
    --no-apt-distupgrade \
    --no-source \
    --verbose \
    --nolog \
    --bd-uninstallable-explainer= \
    --starting-build-commands='grep -iq "^Rules-Requires-Root:" "%p/debian/control" || sed -i "1iRules-Requires-Root: no" "%p/debian/control"' \
    --build-path="/build/path" \
    --dsc-dir="/dsc/dir" \
    "/src/pkg_1.0.dsc"
`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"wget"},
					Privileged: true,
				},
				OutputPath: "pkg_1.0_amd64.deb",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.strategy.GenerateFor(tc.target, tc.env)
			if err != nil {
				t.Fatalf("DebootsnapSbuild.GenerateFor() failed unexpectedly: %v", err)
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("DebootsnapSbuild.GenerateFor() returned diff (-got +want):\n%s", diff)
			}
		})
	}
}
