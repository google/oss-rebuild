// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// SourceNamespace defines where or how a dependency identifier was expressed.
type SourceNamespace string

const (
	NamespaceSoname    SourceNamespace = "soname"    // e.g. "libcgraph.so.6"
	NamespaceDnf       SourceNamespace = "dnf"       // e.g. "graphviz-devel"
	NamespaceYum       SourceNamespace = "yum"       // e.g. "graphviz-devel"
	NamespaceApk       SourceNamespace = "apk"       // e.g. "graphviz-dev"
	NamespaceApt       SourceNamespace = "apt"       // e.g. "graphviz-dev"
	NamespaceConda     SourceNamespace = "conda"     // e.g. "graphviz"
	NamespaceCLib      SourceNamespace = "clib"      // e.g. "cgraph" (from -lcgraph or libraries=["cgraph"])
	NamespacePkgConfig SourceNamespace = "pkgconfig" // e.g. "libcgraph"
	NamespaceBinary    SourceNamespace = "bin"       // e.g. "dot"
)

// DependencyIdentifier represents an extracted system dependency requirement.
type DependencyIdentifier struct {
	Namespace  SourceNamespace `json:"namespace" yaml:"namespace"`
	Name       string          `json:"name" yaml:"name"`
	Provenance string          `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// MappedPackage represents a package manager argument mapped from an identifier.
type MappedPackage struct {
	PackageName string               `json:"package_name"`
	From        DependencyIdentifier `json:"from"`
	Heuristic   bool                 `json:"heuristic,omitempty"`
}

// ResolutionResult holds the mapped package names and any unmappable identifiers.
type ResolutionResult struct {
	TargetOS   rebuild.OS             `json:"target_os"`
	Packages   []MappedPackage        `json:"packages"`
	Unmappable []DependencyIdentifier `json:"unmappable,omitempty"`
}

// PackageNames returns the deduplicated list of package names to be passed to the package manager.
func (r ResolutionResult) PackageNames() []string {
	var names []string
	seen := make(map[string]bool)
	for _, p := range r.Packages {
		if !seen[p.PackageName] {
			seen[p.PackageName] = true
			names = append(names, p.PackageName)
		}
	}
	return names
}

// TargetOSFromPlatformTag determines the target OS category from wheel platform tags.
func TargetOSFromPlatformTag(platformTag string) rebuild.OS {
	switch {
	case strings.Contains(platformTag, "musllinux"):
		return rebuild.OSAlpine
	case strings.Contains(platformTag, "manylinux_2_28"), strings.Contains(platformTag, "manylinux_2_34"):
		return rebuild.OSAlmaLinux
	default:
		return rebuild.OSCentOS
	}
}

// Mapper translates extracted dependency identifiers into target OS package manager arguments.
type Mapper interface {
	Map(targetOS rebuild.OS, ids []DependencyIdentifier) ResolutionResult
}
