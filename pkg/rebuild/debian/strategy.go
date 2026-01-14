// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var (
	binaryVersionRegex = regexp.MustCompile(`^(?P<name>[^_]+)_(?P<nonbinary_version>[^_]+)(\+b\d+)_(?P<arch>[^_]+)\.deb$`)
)

type FileWithChecksum struct {
	URL string `json:"url" yaml:"url,omitempty"`
	MD5 string `json:"md5" yaml:"md5,omitempty"`
}

// DebianPackage aggregates the options controlling a debian package build.
type DebianPackage struct {
	DSC          FileWithChecksum `json:"dsc" yaml:"dsc,omitempty"`
	Orig         FileWithChecksum `json:"orig" yaml:"orig,omitempty"`
	Debian       FileWithChecksum `json:"debian" yaml:"debian,omitempty"`
	Native       FileWithChecksum `json:"native" yaml:"native,omitempty"`
	Requirements []string         `json:"requirements" yaml:"requirements,omitempty"`
}

var _ rebuild.Strategy = &DebianPackage{}

func (b *DebianPackage) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Source: []flow.Step{{
			Uses: "debian/fetch/sources",
			With: map[string]string{
				"dscUrl":    b.DSC.URL,
				"origUrl":   b.Orig.URL,
				"debianUrl": b.Debian.URL,
				"nativeUrl": b.Native.URL,
				"dscMd5":    b.DSC.MD5,
				"origMd5":   b.Orig.MD5,
				"debianMd5": b.Debian.MD5,
				"nativeMd5": b.Native.MD5,
			},
		}},
		Deps: []flow.Step{{
			Uses: "debian/deps/install",
			With: map[string]string{
				"requirements": flow.MustToJSON(b.Requirements),
			},
		}},
		Build: []flow.Step{{
			Uses: "debian/build/debuild",
			With: map[string]string{
				"targetPath": "{{.Target.Artifact}}",
			},
		}},
	}
}

// GenerateFor generates the instructions for a DebianPackage
func (b *DebianPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// Debrebuild uses the upstream's generated buildinfo to perform a rebuild.
type Debrebuild struct {
	BuildInfo  FileWithChecksum `json:"buildinfo" yaml:"buildinfo,omitempty"`
	UseNoCheck bool
}

func (b *Debrebuild) ToWorkflow() *rebuild.WorkflowStrategy {
	s := &rebuild.WorkflowStrategy{
		Source: []flow.Step{{
			Uses: "debian/fetch/buildinfo",
			With: map[string]string{
				"buildinfoUrl": b.BuildInfo.URL,
				"buildinfoMd5": b.BuildInfo.MD5,
			},
		}},
		Deps: []flow.Step{
			{
				Uses: "debian/deps/install-debrebuild",
			},
		},
		Build: []flow.Step{{
			Uses: "debian/build/debrebuild",
			With: map[string]string{
				"targetPath": "{{.Target.Artifact}}",
				"buildinfo":  path.Base(b.BuildInfo.URL),
			},
		}},
		Requires: rebuild.RequiredEnv{
			Privileged: true,
		},
		OutputDir: "out",
	}
	if b.UseNoCheck {
		s.Source = append(s.Source, flow.Step{
			Uses: "debian/source/set-nocheck",
			With: map[string]string{
				"buildinfo": path.Base(b.BuildInfo.URL),
			},
		})
	}
	return s
}

// Generate generates the instructions for a Debrebuild
func (b *Debrebuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// DebootsnapSbuild uses debootsnap sbuild to perform a rebuild.
type DebootsnapSbuild struct {
	BuildInfo                FileWithChecksum `json:"buildinfo" yaml:"buildinfo,omitempty"`
	BuildArchAll             bool             `json:"buildArchAll" yaml:"buildArchAll,omitempty"`
	BuildArchAny             bool             `json:"buildArchAny" yaml:"buildArchAny,omitempty"`
	BuildArch                string           `json:"buildArch" yaml:"buildArch,omitempty"`
	HostArch                 string           `json:"hostArch" yaml:"hostArch,omitempty"`
	SrcPackage               string           `json:"srcPackage" yaml:"srcPackage,omitempty"`
	SrcVersion               string           `json:"srcVersion" yaml:"srcVersion,omitempty"`
	SrcVersionNoEpoch        string           `json:"srcVersionNoEpoch" yaml:"srcVersionNoEpoch,omitempty"`
	BuildPath                string           `json:"buildPath" yaml:"buildPath,omitempty"`
	DscDir                   string           `json:"dscDir" yaml:"dscDir,omitempty"`
	DpkgVersion              string           `json:"dpkgVersion" yaml:"dpkgVersion,omitempty"`
	ForceRulesRequiresRootNo bool             `json:"forceRulesRequiresRootNo" yaml:"forceRulesRequiresRootNo,omitempty"`
	Env                      []string         `json:"env" yaml:"env,omitempty"`
	BinaryOnlyChanges        string           `json:"binaryOnlyChanges" yaml:"binaryOnlyChanges,omitempty"`
}

func (b *DebootsnapSbuild) ToWorkflow() *rebuild.WorkflowStrategy {
	s := &rebuild.WorkflowStrategy{
		Source: []flow.Step{
			{
				Uses: "debian/deps/install",
				With: map[string]string{
					"requirements": flow.MustToJSON([]string{"devscripts", "mmdebstrap", "sbuild"}),
				},
			},
			{
				Uses: "debian/fetch/buildinfo",
				With: map[string]string{
					"buildinfoUrl": b.BuildInfo.URL,
					"buildinfoMd5": b.BuildInfo.MD5,
				},
			},
			{
				Uses: "debian/fetch/dsc",
				With: map[string]string{
					"srcPackage": b.SrcPackage,
					"srcVersion": b.SrcVersion,
				},
			},
		},
		Deps: []flow.Step{
			{
				Uses: "debian/deps/debootsnap",
				With: map[string]string{
					"buildinfo": path.Base(b.BuildInfo.URL),
				},
			},
		},
		Build: []flow.Step{
			{
				// Removes noisy "cannot bind moun /dev/console" errors
				Uses: "debian/build/make-dev-console",
			},
			{
				Uses: "debian/build/sbuild",
				With: map[string]string{
					"env":       strings.Join(b.Env, " "),
					"buildArch": b.BuildArch,
					"hostArch":  b.HostArch,
					"buildArchAll": func() string {
						if b.BuildArchAll {
							return "true"
						}
						return "" // Empty string will evaluate false in the template
					}(),
					"buildArchAny": func() string {
						if b.BuildArchAny {
							return "true"
						}
						return ""
					}(),
					"forceRulesRequiresRootNo": func() string {
						if b.ForceRulesRequiresRootNo {
							return "true"
						}
						return ""
					}(),
					"binNMU":    b.BinaryOnlyChanges,
					"buildPath": b.BuildPath,
					"dscDir":    b.DscDir,
					"dscPath":   path.Join("/", "src", fmt.Sprintf("%s_%s.dsc", b.SrcPackage, b.SrcVersionNoEpoch)),
				},
			},
		},
		Requires: rebuild.RequiredEnv{
			Privileged: true,
		},
		OutputDir: "./",
	}
	return s
}

// GenerateFor generates the instructions for a DebootsnapSbuild
func (b *DebootsnapSbuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var toolkit = []*flow.Tool{
	{
		Name: "debian/fetch/sources",
		Steps: []flow.Step{{
			// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
			Runs: textwrap.Dedent(`
				wget {{.With.dscUrl}}
				{{- if ne .With.nativeUrl "" }}
				wget {{.With.nativeUrl}}
				{{ else }}
				wget {{.With.origUrl}}
				wget {{.With.debianUrl}}
				{{ end }}
				dpkg-source -x --no-check $(basename "{{.With.dscUrl}}")`)[1:],
			Needs: []string{"wget", "git"},
		}},
	},
	{
		Name: "debian/fetch/buildinfo",
		Steps: []flow.Step{{
			// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
			Runs: textwrap.Dedent(`
				wget {{.With.buildinfoUrl}}`)[1:],
			Needs: []string{"wget"},
		}},
	},
	{
		Name: "debian/fetch/dsc",
		Steps: []flow.Step{{
			Runs: `debsnap --force --verbose --destdir ./ {{.With.srcPackage}} {{.With.srcVersion}}`,
		}},
	},
	{
		Name: "debian/source/set-nocheck",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				BUILDINFO_FILE='{{ .With.buildinfo }}'
				NEW_PROFILE_ENTRY='DEB_BUILD_PROFILES="nocheck"'
				if grep -q "DEB_BUILD_PROFILES=" "$BUILDINFO_FILE"; then
				    sed -i '/^\(Environment:\|\s\+\)DEB_BUILD_PROFILES=/s/DEB_BUILD_PROFILES=.*$/'"$NEW_PROFILE_ENTRY"'/' "$BUILDINFO_FILE"
				else
				    sed -i '/^Environment:/a \ '"$NEW_PROFILE_ENTRY"'' "$BUILDINFO_FILE"
				fi`)[1:],
		}},
	},
	{
		Name: "debian/deps/install",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				apt update
				apt install -y{{range $req := .With.requirements | fromJSON}} {{$req}}{{end}}`)[1:],
		}},
	},
	{
		Name: "debian/deps/install-debrebuild",
		Steps: []flow.Step{{
			// TODO: pin these versions
			Runs: textwrap.Dedent(`
				apt -o Acquire::Check-Valid-Until=false update
				apt install -y devscripts mmdebstrap sbuild`[1:]),
		}},
	},
	{
		Name: "debian/build/handle-binary-version",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- $expected := regexReplace .With.targetPath "\\+b[0-9]+(_[^_]+\\.deb)$" "$1"}}
				{{- if ne $expected .With.targetPath }}mv /src/{{$expected}} /src/{{.With.targetPath}}
				{{- end}}`)[1:],
		}},
	},
	{
		Name: "debian/build/debuild",
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
					cd */
					debuild -b -uc -us`)[1:],
				Needs: []string{"build-essential", "fakeroot", "devscripts"},
			},
			{
				Uses: "debian/build/handle-binary-version",
				With: map[string]string{
					"targetPath": "{{.With.targetPath}}",
				},
			},
		},
	},
	{
		Name: "debian/deps/debootsnap",
		Steps: []flow.Step{{
			Runs: `debootsnap --buildinfo="{{ .With.buildinfo }}" "/tmp/chroot.tar"`,
		}},
	},
	{
		Name: "debian/build/make-dev-console",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
			if [ ! -e /dev/console ]; then
				mknod -m 666 /dev/console c 5 1
			fi`)[1:],
		}},
	},
	{
		Name: "debian/build/sbuild",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
cat <<"EOFSTEP" > "/tmp/sbuild.config"
$apt_get = '/bin/true';
$apt_cache = '/bin/true';
$build_as_root_when_needed = 1;
EOFSTEP
echo "root:100000:65536" > /etc/subuid
echo "root:100000:65536" > /etc/subgid
env --chdir=/src {{.With.env}} SBUILD_CONFIG=/tmp/sbuild.config\
    sbuild \
    --build={{ .With.buildArch }} \
    --host={{ .With.hostArch }} \
    {{ if .With.buildArchAny }}--arch-any{{ else }}--no-arch-any{{ end }} \
    {{ if .With.buildArchAll }}--arch-all{{ else }}--no-arch-all{{ end }} \
    {{ if .With.binNMU -}}
    --binNMU-changelog="{{ .With.binNMU }}" \
    {{ end -}}
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
    {{ if .With.forceRulesRequiresRootNo -}}
    --starting-build-commands='grep -iq "^Rules-Requires-Root:" "%p/debian/control" || sed -i "1iRules-Requires-Root: no" "%p/debian/control"' \
    {{ end -}}
    {{ if .With.buildPath }}--build-path="{{ .With.buildPath }}"{{ end }} \
    {{ if .With.dscDir }}--dsc-dir="{{ .With.dscDir }}"{{ end }} \
    "{{ .With.dscPath }}"
`)[1:],
		}},
	},
	{
		Name: "debian/build/debrebuild",
		Steps: []flow.Step{
			{
				// The subuid and subgid mappings are needed for unshare to create a new namespace for the build.
				// This setup assumes:
				//	1. You're running the build as root
				//  2. This mapping doesn't yet exist
				// Both those things are true when executing in a standard debian container, but might not be true in other environments.
				Runs: textwrap.Dedent(`
				echo "root:100000:65536" > /etc/subuid
				echo "root:100000:65536" > /etc/subgid
				debrebuild --buildresult=./out --builder=sbuild+unshare {{ .With.buildinfo }}`[1:]),
			},
		},
	}}
