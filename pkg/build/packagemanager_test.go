// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"testing"
)

func TestPackageManagerCommands_InstallCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      PackageManagerCommands
		packages []string
		want     string
	}{
		{
			name: "Alpine with single package",
			cmd: PackageManagerCommands{
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
			packages: []string{"curl"},
			want:     "apk add curl",
		},
		{
			name: "Alpine with multiple packages",
			cmd: PackageManagerCommands{
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
			packages: []string{"curl", "wget", "git"},
			want:     "apk add curl wget git",
		},
		{
			name: "Ubuntu with packages",
			cmd: PackageManagerCommands{
				InstallCmd:  "apt install",
				InstallArgs: []string{"-y"},
			},
			packages: []string{"python3", "python3-pip"},
			want:     "apt install -y python3 python3-pip",
		},
		{
			name: "Empty package list",
			cmd: PackageManagerCommands{
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
			packages: []string{},
			want:     "apk add",
		},
		{
			name: "No install args",
			cmd: PackageManagerCommands{
				InstallCmd:  "pacman -S",
				InstallArgs: []string{},
			},
			packages: []string{"vim"},
			want:     "pacman -S vim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cmd.InstallCommand(tt.packages)
			if got != tt.want {
				t.Errorf("InstallCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPackageManagerCommands(t *testing.T) {
	tests := []struct {
		name string
		os   OS
		want PackageManagerCommands
	}{
		{
			name: "Alpine",
			os:   Alpine,
			want: PackageManagerCommands{
				UpdateCmd:   "apk update",
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
		},
		{
			name: "Debian",
			os:   Debian,
			want: PackageManagerCommands{
				UpdateCmd:   "apt update",
				InstallCmd:  "apt install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "Ubuntu",
			os:   Ubuntu,
			want: PackageManagerCommands{
				UpdateCmd:   "apt update",
				InstallCmd:  "apt install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "CentOS",
			os:   CentOS,
			want: PackageManagerCommands{
				UpdateCmd:   "yum update -y",
				InstallCmd:  "yum install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "Unknown OS defaults to Alpine",
			os:   OS("unknown"),
			want: PackageManagerCommands{
				UpdateCmd:   "apk update",
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPackageManagerCommands(tt.os)
			if got.UpdateCmd != tt.want.UpdateCmd {
				t.Errorf("GetPackageManagerCommands().UpdateCmd = %v, want %v", got.UpdateCmd, tt.want.UpdateCmd)
			}
			if got.InstallCmd != tt.want.InstallCmd {
				t.Errorf("GetPackageManagerCommands().InstallCmd = %v, want %v", got.InstallCmd, tt.want.InstallCmd)
			}
			if len(got.InstallArgs) != len(tt.want.InstallArgs) {
				t.Errorf("GetPackageManagerCommands().InstallArgs length = %v, want %v", len(got.InstallArgs), len(tt.want.InstallArgs))
			}
			for i, arg := range got.InstallArgs {
				if i < len(tt.want.InstallArgs) && arg != tt.want.InstallArgs[i] {
					t.Errorf("GetPackageManagerCommands().InstallArgs[%d] = %v, want %v", i, arg, tt.want.InstallArgs[i])
				}
			}
		})
	}
}

func TestDetectOS(t *testing.T) {
	tests := []struct {
		name      string
		baseImage string
		want      OS
	}{
		{
			name:      "Alpine latest",
			baseImage: "alpine:latest",
			want:      Alpine,
		},
		{
			name:      "Alpine with version",
			baseImage: "alpine:3.19",
			want:      Alpine,
		},
		{
			name:      "Alpine in compound name",
			baseImage: "node:18-alpine",
			want:      Alpine,
		},
		{
			name:      "Debian latest",
			baseImage: "debian:latest",
			want:      Debian,
		},
		{
			name:      "Debian with version",
			baseImage: "debian:bullseye",
			want:      Debian,
		},
		{
			name:      "Ubuntu latest",
			baseImage: "ubuntu:latest",
			want:      Ubuntu,
		},
		{
			name:      "Ubuntu with version",
			baseImage: "ubuntu:22.04",
			want:      Ubuntu,
		},
		{
			name:      "Ubuntu in compound name",
			baseImage: "python:3.11-ubuntu",
			want:      Ubuntu,
		},
		{
			name:      "CentOS",
			baseImage: "centos:7",
			want:      CentOS,
		},
		{
			name:      "CentOS latest",
			baseImage: "centos:latest",
			want:      CentOS,
		},
		{
			name:      "RHEL",
			baseImage: "rhel:8",
			want:      CentOS,
		},
		{
			name:      "RHEL UBI",
			baseImage: "registry.redhat.io/ubi8/rhel",
			want:      CentOS,
		},
		{
			name:      "Unknown image defaults to Alpine",
			baseImage: "scratch",
			want:      Alpine,
		},
		{
			name:      "Random image defaults to Alpine",
			baseImage: "busybox:latest",
			want:      Alpine,
		},
		{
			name:      "Empty string defaults to Alpine",
			baseImage: "",
			want:      Alpine,
		},
		{
			name:      "Case sensitivity test",
			baseImage: "ALPINE:latest",
			want:      Alpine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectOS(tt.baseImage)
			if got != tt.want {
				t.Errorf("DetectOS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOSConstants(t *testing.T) {
	// Test that OS constants have expected values
	tests := []struct {
		name string
		os   OS
		want string
	}{
		{"Alpine constant", Alpine, "alpine"},
		{"Debian constant", Debian, "debian"},
		{"Ubuntu constant", Ubuntu, "ubuntu"},
		{"CentOS constant", CentOS, "centos"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.os) != tt.want {
				t.Errorf("OS constant %v = %v, want %v", tt.name, string(tt.os), tt.want)
			}
		})
	}
}
