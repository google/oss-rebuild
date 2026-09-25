// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

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
			os:   OSAlpine,
			want: PackageManagerCommands{
				UpdateCmd:   "apk update",
				InstallCmd:  "apk add",
				InstallArgs: []string{},
			},
		},
		{
			name: "Debian",
			os:   OSDebian,
			want: PackageManagerCommands{
				UpdateCmd:   "apt update",
				InstallCmd:  "apt install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "Ubuntu",
			os:   OSUbuntu,
			want: PackageManagerCommands{
				UpdateCmd:   "apt update",
				InstallCmd:  "apt install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "CentOS",
			os:   OSCentOS,
			want: PackageManagerCommands{
				UpdateCmd:   "yum update -y",
				InstallCmd:  "yum install",
				InstallArgs: []string{"-y"},
			},
		},
		{
			name: "AlmaLinux",
			os:   OSAlmaLinux,
			want: PackageManagerCommands{
				UpdateCmd:   "dnf update -y",
				InstallCmd:  "dnf install",
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
			want:      OSAlpine,
		},
		{
			name:      "Alpine with version",
			baseImage: "alpine:3.19",
			want:      OSAlpine,
		},
		{
			name:      "Alpine in compound name",
			baseImage: "node:18-alpine",
			want:      OSAlpine,
		},
		{
			name:      "Debian latest",
			baseImage: "debian:latest",
			want:      OSDebian,
		},
		{
			name:      "Debian with version",
			baseImage: "debian:bullseye",
			want:      OSDebian,
		},
		{
			name:      "Ubuntu latest",
			baseImage: "ubuntu:latest",
			want:      OSUbuntu,
		},
		{
			name:      "Ubuntu with version",
			baseImage: "ubuntu:22.04",
			want:      OSUbuntu,
		},
		{
			name:      "Ubuntu in compound name",
			baseImage: "python:3.11-ubuntu",
			want:      OSUbuntu,
		},
		{
			name:      "CentOS",
			baseImage: "centos:7",
			want:      OSCentOS,
		},
		{
			name:      "CentOS latest",
			baseImage: "centos:latest",
			want:      OSCentOS,
		},
		{
			name:      "RHEL",
			baseImage: "rhel:8",
			want:      OSCentOS,
		},
		{
			name:      "RHEL UBI",
			baseImage: "registry.redhat.io/ubi8/rhel",
			want:      OSCentOS,
		},
		{
			name:      "manylinux2014",
			baseImage: "quay.io/pypa/manylinux2014_x86_64",
			want:      OSCentOS,
		},
		{
			name:      "manylinux_2_28",
			baseImage: "quay.io/pypa/manylinux_2_28_x86_64",
			want:      OSAlmaLinux,
		},
		{
			name:      "manylinux_2_34",
			baseImage: "quay.io/pypa/manylinux_2_34_x86_64",
			want:      OSAlmaLinux,
		},
		{
			name:      "musllinux_1_1",
			baseImage: "quay.io/pypa/musllinux_1_1_x86_64",
			want:      OSAlpine,
		},
		{
			name:      "musllinux_1_2",
			baseImage: "quay.io/pypa/musllinux_1_2_x86_64",
			want:      OSAlpine,
		},
		{
			name:      "AlmaLinux",
			baseImage: "docker.io/library/almalinux:8",
			want:      OSAlmaLinux,
		},
		{
			name:      "Unknown image defaults to Alpine",
			baseImage: "scratch",
			want:      OSAlpine,
		},
		{
			name:      "Random image defaults to Alpine",
			baseImage: "busybox:latest",
			want:      OSAlpine,
		},
		{
			name:      "Empty string defaults to Alpine",
			baseImage: "",
			want:      OSAlpine,
		},
		{
			name:      "Case sensitivity test",
			baseImage: "ALPINE:latest",
			want:      OSAlpine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapOS(tt.baseImage)
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
		{"Alpine constant", OSAlpine, "alpine"},
		{"Debian constant", OSDebian, "debian"},
		{"Ubuntu constant", OSUbuntu, "ubuntu"},
		{"CentOS constant", OSCentOS, "centos"},
		{"AlmaLinux constant", OSAlmaLinux, "almalinux"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.os) != tt.want {
				t.Errorf("OS constant %v = %v, want %v", tt.name, string(tt.os), tt.want)
			}
		})
	}
}
