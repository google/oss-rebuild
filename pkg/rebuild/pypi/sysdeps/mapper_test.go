// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestTargetOSFromPlatformTag(t *testing.T) {
	tests := []struct {
		tag    string
		wantOS rebuild.OS
	}{
		{"manylinux2014_x86_64", rebuild.OSCentOS},
		{"manylinux1_x86_64", rebuild.OSCentOS},
		{"manylinux_2_28_x86_64", rebuild.OSAlmaLinux},
		{"manylinux_2_34_x86_64", rebuild.OSAlmaLinux},
		{"musllinux_1_1_x86_64", rebuild.OSAlpine},
		{"musllinux_1_2_x86_64", rebuild.OSAlpine},
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			got := TargetOSFromPlatformTag(tc.tag)
			if got != tc.wantOS {
				t.Errorf("TargetOSFromPlatformTag(%q) = %q, want %q", tc.tag, got, tc.wantOS)
			}
		})
	}
}

func TestSimpleMapper(t *testing.T) {
	mapper := SimpleMapper{}

	tests := []struct {
		name     string
		targetOS rebuild.OS
		ids      []DependencyIdentifier
		want     ResolutionResult
	}{
		{
			name:     "NativeVirtualSpecifiersCentOS",
			targetOS: rebuild.OSCentOS,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceBinary, Name: "dot", Provenance: "setup.py"},
				{Namespace: NamespacePkgConfig, Name: "libcgraph", Provenance: "pkg-config"},
				{Namespace: NamespaceSoname, Name: "libcgraph.so.6", Provenance: "wheel.libs"},
				{Namespace: NamespaceCLib, Name: "cgraph", Provenance: "setup.py"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSCentOS,
				Packages: []MappedPackage{
					{PackageName: "/usr/bin/dot", From: DependencyIdentifier{Namespace: NamespaceBinary, Name: "dot", Provenance: "setup.py"}},
					{PackageName: "pkgconfig(libcgraph)", From: DependencyIdentifier{Namespace: NamespacePkgConfig, Name: "libcgraph", Provenance: "pkg-config"}},
					{PackageName: "libcgraph.so.6()(64bit)", From: DependencyIdentifier{Namespace: NamespaceSoname, Name: "libcgraph.so.6", Provenance: "wheel.libs"}},
					{PackageName: "/usr/lib64/libcgraph.so", From: DependencyIdentifier{Namespace: NamespaceCLib, Name: "cgraph", Provenance: "setup.py"}},
				},
			},
		},
		{
			name:     "NativeVirtualSpecifiersAlpine",
			targetOS: rebuild.OSAlpine,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceBinary, Name: "dot"},
				{Namespace: NamespacePkgConfig, Name: "libcgraph"},
				{Namespace: NamespaceSoname, Name: "libcgraph.so.6"},
				{Namespace: NamespaceCLib, Name: "cgraph"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSAlpine,
				Packages: []MappedPackage{
					{PackageName: "cmd:dot", From: DependencyIdentifier{Namespace: NamespaceBinary, Name: "dot"}},
					{PackageName: "pc:libcgraph", From: DependencyIdentifier{Namespace: NamespacePkgConfig, Name: "libcgraph"}},
					{PackageName: "so:libcgraph.so.6", From: DependencyIdentifier{Namespace: NamespaceSoname, Name: "libcgraph.so.6"}},
					{PackageName: "cgraph-dev", From: DependencyIdentifier{Namespace: NamespaceCLib, Name: "cgraph"}, Heuristic: true},
				},
			},
		},
		{
			name:     "SameOSPassThrough",
			targetOS: rebuild.OSAlmaLinux,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceDnf, Name: "graphviz-devel"},
				{Namespace: NamespaceYum, Name: "zlib-devel"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSAlmaLinux,
				Packages: []MappedPackage{
					{PackageName: "graphviz-devel", From: DependencyIdentifier{Namespace: NamespaceDnf, Name: "graphviz-devel"}},
					{PackageName: "zlib-devel", From: DependencyIdentifier{Namespace: NamespaceYum, Name: "zlib-devel"}},
				},
			},
		},
		{
			name:     "DnfToAlpineHeuristic",
			targetOS: rebuild.OSAlpine,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceDnf, Name: "graphviz-devel"},
				{Namespace: NamespaceYum, Name: "graphviz"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSAlpine,
				Packages: []MappedPackage{
					{PackageName: "graphviz-dev", From: DependencyIdentifier{Namespace: NamespaceDnf, Name: "graphviz-devel"}, Heuristic: true},
					{PackageName: "graphviz", From: DependencyIdentifier{Namespace: NamespaceYum, Name: "graphviz"}, Heuristic: true},
				},
			},
		},
		{
			name:     "AptToCentOSHeuristic",
			targetOS: rebuild.OSCentOS,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceApt, Name: "graphviz-dev"},
				{Namespace: NamespaceApt, Name: "graphviz"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSCentOS,
				Packages: []MappedPackage{
					{PackageName: "graphviz-devel", From: DependencyIdentifier{Namespace: NamespaceApt, Name: "graphviz-dev"}, Heuristic: true},
					{PackageName: "graphviz", From: DependencyIdentifier{Namespace: NamespaceApt, Name: "graphviz"}, Heuristic: true},
				},
			},
		},
		{
			name:     "AptToAlpineHeuristic",
			targetOS: rebuild.OSAlpine,
			ids: []DependencyIdentifier{
				{Namespace: NamespaceApt, Name: "graphviz-dev"},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSAlpine,
				Packages: []MappedPackage{
					{PackageName: "graphviz-dev", From: DependencyIdentifier{Namespace: NamespaceApt, Name: "graphviz-dev"}, Heuristic: true},
				},
			},
		},
		{
			name:     "UnmappableAndEmpty",
			targetOS: rebuild.OSCentOS,
			ids: []DependencyIdentifier{
				{Namespace: "unknown_ns", Name: "some-pkg"},
				{Namespace: NamespaceBinary, Name: ""},
			},
			want: ResolutionResult{
				TargetOS: rebuild.OSCentOS,
				Unmappable: []DependencyIdentifier{
					{Namespace: "unknown_ns", Name: "some-pkg"},
					{Namespace: NamespaceBinary, Name: ""},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapper.Map(tc.targetOS, tc.ids)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SimpleMapper.Map() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPackageNamesDeduplication(t *testing.T) {
	res := ResolutionResult{
		Packages: []MappedPackage{
			{PackageName: "pkg-a"},
			{PackageName: "pkg-b"},
			{PackageName: "pkg-a"},
		},
	}

	got := res.PackageNames()
	want := []string{"pkg-a", "pkg-b"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PackageNames() returned diff (-want +got):\n%s", diff)
	}
}
