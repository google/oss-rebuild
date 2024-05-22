// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
}

// PackageManifest is the [package] section of the Cargo.toml file.
type PackageManifest struct {
	Name         string `toml:"name"`
	RawVersion   any    `toml:"version"`
	RawWorkspace any    `toml:"workspace"`
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
