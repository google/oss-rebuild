// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"path"
	"regexp"

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

// UpstreamSourceArchive builds an orig tarball from an upstream git repository.
// This enables attestable provenance from upstream git through to the source package.
type UpstreamSourceArchive struct {
	// Source git repository and ref
	Location rebuild.Location `json:"location" yaml:"location"`

	// Compression format: "xz", "gz", "bz2"
	Compression string `json:"compression" yaml:"compression"`

	// Directory prefix in tarball (e.g., "xz-5.2.4/")
	// If empty, inferred as "<package>-<version>/"
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`

	// Output filename (e.g., "xz-utils_5.2.4.orig.tar.xz")
	OutputFilename string `json:"output_filename" yaml:"output_filename"`
}

var _ rebuild.Strategy = &UpstreamSourceArchive{}

func (b *UpstreamSourceArchive) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Build: []flow.Step{{
			Uses: "debian/build/orig-from-git",
			With: map[string]string{
				"prefix":         b.Prefix,
				"compression":    b.Compression,
				"outputFilename": b.OutputFilename,
			},
		}},
		OutputPath: b.OutputFilename,
	}
}

// GenerateFor generates the instructions for an UpstreamOrig
func (b *UpstreamSourceArchive) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var toolkit = []*flow.Tool{
	{
		Name: "debian/build/orig-from-git",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
        {{- /* Determine compression command at template resolution time */ -}}
        {{- $compressCmd := "" -}}
        {{- if eq .With.compression "xz" }}{{ $compressCmd = "xz -c" -}}
        {{- else if eq .With.compression "gz" }}{{ $compressCmd = "gzip -c" -}}
        {{- else if eq .With.compression "bz2" }}{{ $compressCmd = "bzip2 -c" -}}
        {{- end -}}

        {{- $opts := "--format=tar" -}}
        {{- if ne .With.prefix "" }}{{ $opts = printf "%s --prefix=%s" $opts .With.prefix }}{{ end -}}

        {{- /* Generate the final shell command */ -}}
        git archive {{ $opts }} '{{.Location.Ref}}'
        {{- if ne .Location.Dir "" }} -- '{{.Location.Dir}}'{{- end }} | {{ $compressCmd }} > "{{.With.outputFilename}}"`)[1:],
			Needs: []string{"git", "xz-utils", "gzip", "bzip2"},
		}},
	},
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
	},
}
