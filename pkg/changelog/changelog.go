// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package changelog

import (
	_ "embed"

	"gopkg.in/yaml.v3"
)

//go:embed changelog.yaml
var changelogYAML []byte

// Builtin is the list of service updates impacting attesttaions as parsed from the embedded YAML.
var Builtin []ChangelogEntry

func init() {
	if err := yaml.Unmarshal(changelogYAML, &Builtin); err != nil {
		panic(err)
	}
}

// ChangelogEntry represents a single impactful change in the service history.
type ChangelogEntry struct {
	Version string `yaml:"version"`
	Reason  string `yaml:"reason"`
}

// EntryOnInterval returns true if there is a documented change strictly after oldVersion and up to/including newVersion.
// It relies on lexicographical comparison of Golang pseudo-versions.
func EntryOnInterval(oldVersion, newVersion string) bool {
	for _, entry := range Builtin {
		// Check if oldVersion < entry.Version <= newVersion
		if entry.Version > oldVersion && entry.Version <= newVersion {
			return true
		}
	}
	return false
}
