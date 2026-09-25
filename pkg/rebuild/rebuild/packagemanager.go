// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"slices"
	"strings"
)

// OS represents a supported operating system/distribution
type OS string

const (
	OSAlpine    OS = "alpine"
	OSDebian    OS = "debian"
	OSUbuntu    OS = "ubuntu"
	OSCentOS    OS = "centos"
	OSAlmaLinux OS = "almalinux"
)

// PackageManagerCommands contains the commands needed for package management on a specific OS
type PackageManagerCommands struct {
	UpdateCmd   string
	InstallCmd  string
	InstallArgs []string
}

// InstallCommand generates the full package installation command for the given packages
func (p PackageManagerCommands) InstallCommand(packages []string) string {
	cmdArgs := slices.Concat([]string{p.InstallCmd}, p.InstallArgs, packages)
	return strings.Join(cmdArgs, " ")
}

// osPackageManagers maps operating systems to their package manager commands
var osPackageManagers = map[OS]PackageManagerCommands{
	OSAlpine: {
		UpdateCmd:  "apk update",
		InstallCmd: "apk add",
		// TODO: Add --no-cache
		InstallArgs: []string{},
	},
	OSDebian: {
		UpdateCmd:  "apt update",
		InstallCmd: "apt install",
		// TODO: Add --no-install-recommends
		InstallArgs: []string{"-y"},
	},
	OSUbuntu: {
		UpdateCmd:   "apt update",
		InstallCmd:  "apt install",
		InstallArgs: []string{"-y"},
	},
	OSCentOS: {
		UpdateCmd:   "yum update -y",
		InstallCmd:  "yum install",
		InstallArgs: []string{"-y"},
	},
	OSAlmaLinux: {
		UpdateCmd:   "dnf update -y",
		InstallCmd:  "dnf install",
		InstallArgs: []string{"-y"},
	},
}

// GetPackageManagerCommands returns the package manager commands for the given OS
func GetPackageManagerCommands(os OS) PackageManagerCommands {
	if cmd, ok := osPackageManagers[os]; ok {
		return cmd
	}
	return osPackageManagers[OSAlpine] // Not necessarily accurate but generally a safe assumption
}

// MapOS maps the base image name to an operating system
func MapOS(baseImage string) OS {
	switch {
	case strings.Contains(baseImage, "alpine"), strings.Contains(baseImage, "musllinux"), strings.Contains(baseImage, "library/docker"):
		return OSAlpine
	case strings.Contains(baseImage, "debian"):
		return OSDebian
	case strings.Contains(baseImage, "ubuntu"):
		return OSUbuntu
	case strings.Contains(baseImage, "manylinux_2_28"), strings.Contains(baseImage, "manylinux_2_34"), strings.Contains(baseImage, "almalinux"), strings.Contains(baseImage, "fedora"):
		return OSAlmaLinux
	case strings.Contains(baseImage, "centos"), strings.Contains(baseImage, "rhel"), strings.Contains(baseImage, "manylinux"):
		return OSCentOS
	default:
		return OSAlpine // safe default
	}
}
