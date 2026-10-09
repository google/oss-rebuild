// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// SimpleMapper provides basic offline mapping of dependency identifiers to target OS packages.
//
// It leverages the following capabilities of the supported package managers for its mapping.
// - `yum`, `dnf` and `apk` can identify a package given a shared object identifier
// - `yum`, `dnf` and `apk` can identify a package given a pkg-config identifier
// - `yum` and `dnf` can identify a package given a file path it installs
// - `apk` can identify a package given the command it provides
type SimpleMapper struct{}

// DefaultMapper is the default instance of SimpleMapper.
var DefaultMapper Mapper = SimpleMapper{}

// Map maps a slice of DependencyIdentifier structs to package manager arguments for targetOS.
func (m SimpleMapper) Map(targetOS rebuild.OS, ids []DependencyIdentifier) ResolutionResult {
	res := ResolutionResult{
		TargetOS: targetOS,
	}

	for _, id := range ids {
		if strings.TrimSpace(id.Name) == "" {
			res.Unmappable = append(res.Unmappable, id)
			continue
		}

		switch id.Namespace {
		case NamespaceBinary:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "cmd:" + id.Name,
					From:        id,
				})
			default: // centos, almalinux
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "/usr/bin/" + id.Name,
					From:        id,
				})
			}

		case NamespacePkgConfig:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "pc:" + id.Name,
					From:        id,
				})
			default: // centos, almalinux
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "pkgconfig(" + id.Name + ")",
					From:        id,
				})
			}

		case NamespaceSoname:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "so:" + id.Name,
					From:        id,
				})
			default: // centos, almalinux
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: id.Name + "()(64bit)",
					From:        id,
				})
			}

		case NamespaceCLib:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: id.Name + "-dev",
					From:        id,
					Heuristic:   true,
				})
			default: // centos, almalinux
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: "/usr/lib64/lib" + id.Name + ".so",
					From:        id,
				})
			}

		case NamespaceYum, NamespaceDnf:
			switch targetOS {
			case rebuild.OSAlpine:
				name := id.Name
				if stem, found := strings.CutSuffix(name, "-devel"); found {
					name = stem + "-dev"
				}
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: name,
					From:        id,
					Heuristic:   true,
				})
			default: // centos, almalinux
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: id.Name,
					From:        id,
				})
			}

		case NamespaceApk:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: id.Name,
					From:        id,
				})
			default: // centos, almalinux
				name := id.Name
				if stem, found := strings.CutSuffix(name, "-dev"); found {
					name = stem + "-devel"
				}
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: name,
					From:        id,
					Heuristic:   true,
				})
			}

		case NamespaceApt:
			switch targetOS {
			case rebuild.OSAlpine:
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: id.Name,
					From:        id,
					Heuristic:   true,
				})
			default: // centos, almalinux
				name := id.Name
				if stem, found := strings.CutSuffix(name, "-dev"); found {
					name = stem + "-devel"
				}
				res.Packages = append(res.Packages, MappedPackage{
					PackageName: name,
					From:        id,
					Heuristic:   true,
				})
			}

		default:
			// Namespaces not yet supported by SimpleMapper are recorded as unmappable
			res.Unmappable = append(res.Unmappable, id)
		}
	}

	return res
}
