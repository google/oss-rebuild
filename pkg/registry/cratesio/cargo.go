// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

// CargoVCSInfo abstracts the contents of the .cargo_vcs_info.json file included in published crates.
//
// Format: https://doc.rust-lang.org/cargo/commands/cargo-package.html#cargo_vcs_infojson-format
type CargoVCSInfo struct {
	GitInfo `json:"git"`
	Dir     string `json:"path_in_vcs"`
}

// GitInfo is the Git metadata included in the .cargo_vcs_info.json file.
type GitInfo struct {
	SHA1 string `json:"sha1"`
}

// CargoTOML provides a subset of the Cargo.toml file format used for crate metadata.
//
// Format: https://doc.rust-lang.org/cargo/reference/manifest.html
type CargoTOML struct {
	PackageManifest `toml:"package"`
	Workspace       *WorkspaceManifest `toml:"workspace"`
}

// PackageManifest is the [package] section of the Cargo.toml file.
type PackageManifest struct {
	Name         string `toml:"name"`
	RawVersion   any    `toml:"version"`
	RawWorkspace any    `toml:"workspace"`
}

// WorkspaceManifest is the [workspace] section of the Cargo.toml file.
type WorkspaceManifest struct {
	Package WorkspacePackageManifest `toml:"package"`
}

// WorkspacePackageManifest is the [workspace.package] section of the Cargo.toml file.
type WorkspacePackageManifest struct {
	Version string `toml:"version"`
}

// WorkspaceVersion is the special version string used for workspace crates.
const WorkspaceVersion = "workspace"

// Version returns the version string for the package or WorkspaceVersion if a workspace crate.
func (pm PackageManifest) Version() string {
	if v, ok := pm.RawVersion.(string); ok {
		return v
	} else if _, ok := pm.RawVersion.(map[string]any); ok {
		return WorkspaceVersion
	} else {
		return ""
	}
}

// WorkspacePath returns the explicit package.workspace path, if set.
func (pm PackageManifest) WorkspacePath() string {
	if p, ok := pm.RawWorkspace.(string); ok {
		return p
	}
	return ""
}
