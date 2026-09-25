// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// OS represents a supported operating system/distribution
type OS = rebuild.OS

const (
	Alpine    OS = rebuild.OSAlpine
	Debian    OS = rebuild.OSDebian
	Ubuntu    OS = rebuild.OSUbuntu
	CentOS    OS = rebuild.OSCentOS
	AlmaLinux OS = rebuild.OSAlmaLinux
)

// PackageManagerCommands contains the commands needed for package management on a specific OS
type PackageManagerCommands = rebuild.PackageManagerCommands

var (
	// GetPackageManagerCommands returns the package manager commands for the given OS
	GetPackageManagerCommands = rebuild.GetPackageManagerCommands

	// DetectOS detects the OS from a base image name
	DetectOS = rebuild.MapOS
)
