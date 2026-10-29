// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"slices"
	"strings"

	"github.com/google/oss-rebuild/internal/shell"
)

// OS represents a supported operating system/distribution
type OS string

const (
	Alpine OS = "alpine"
	Debian OS = "debian"
	Ubuntu OS = "ubuntu"
	CentOS OS = "centos"
)

// PackageManagerCommands contains the commands needed for package management on a specific OS
type PackageManagerCommands struct {
	UpdateCmd   string
	InstallCmd  string
	InstallArgs []string
}

// InstallCommand generates the full package installation command for the given packages
func (p PackageManagerCommands) InstallCommand(packages []string) string {
	quoted := make([]string, len(packages))
	for i, pkg := range packages {
		quoted[i] = shell.Quote(pkg)
	}
	cmdArgs := slices.Concat([]string{p.InstallCmd}, p.InstallArgs, quoted)
	return strings.Join(cmdArgs, " ")
}

// osPackageManagers maps operating systems to their package manager commands
var osPackageManagers = map[OS]PackageManagerCommands{
	Alpine: {
		UpdateCmd:  "apk update",
		InstallCmd: "apk add",
		// TODO: Add --no-cache
		InstallArgs: []string{},
	},
	Debian: {
		UpdateCmd:  "apt update",
		InstallCmd: "apt install",
		// TODO: Add --no-install-recommends
		InstallArgs: []string{"-y"},
	},
	Ubuntu: {
		UpdateCmd:   "apt update",
		InstallCmd:  "apt install",
		InstallArgs: []string{"-y"},
	},
	CentOS: {
		// NOTE: Unlike apt and apk, "yum update" upgrades the installed packages,
		// which replaces the compiler and libraries that the base image provides.
		// makecache only refreshes the package metadata.
		UpdateCmd:   "yum makecache",
		InstallCmd:  "yum install",
		InstallArgs: []string{"-y"},
	},
}

// GetPackageManagerCommands returns the package manager commands for the given OS
func GetPackageManagerCommands(os OS) PackageManagerCommands {
	if cmd, ok := osPackageManagers[os]; ok {
		return cmd
	}
	return osPackageManagers[Alpine] // Not necessarily accurate but generally a safe assumption
}

// DetectOS detects the OS from a base image name
func DetectOS(baseImage string) OS {
	switch {
	case strings.Contains(baseImage, "alpine"), strings.Contains(baseImage, "musllinux"), strings.Contains(baseImage, "library/docker"):
		return Alpine
	case strings.Contains(baseImage, "debian"):
		return Debian
	case strings.Contains(baseImage, "ubuntu"):
		return Ubuntu
	case strings.Contains(baseImage, "centos"), strings.Contains(baseImage, "rhel"), strings.Contains(baseImage, "manylinux"), strings.Contains(baseImage, "almalinux"):
		return CentOS
	default:
		return Alpine // safe default
	}
}
