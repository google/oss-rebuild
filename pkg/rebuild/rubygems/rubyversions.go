// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"strings"

	"github.com/pkg/errors"
)

// rubyRelease maps a Ruby version to the RubyGems version it bundles.
// Data sourced from Ruby source tags (lib/rubygems.rb VERSION constant).
// Only includes versions available from ruby-builder.
type rubyRelease struct {
	Ruby     string
	RubyGems string
}

// rubyReleases lists Ruby versions and their bundled RubyGems versions,
// ordered by Ruby version ascending.
var rubyReleases = []rubyRelease{
	// Ruby 2.5.x
	{"2.5.0", "2.7.3"},
	{"2.5.1", "2.7.6"},
	{"2.5.2", "2.7.6"},
	{"2.5.3", "2.7.6"},
	{"2.5.4", "2.7.6"},
	{"2.5.5", "2.7.6"},
	{"2.5.6", "2.7.6"},
	{"2.5.7", "2.7.6"},
	{"2.5.8", "2.7.6"},
	{"2.5.9", "2.7.6.3"},
	// Ruby 2.6.x
	{"2.6.0", "3.0.1"},
	{"2.6.1", "3.0.1"},
	{"2.6.2", "3.0.1"},
	{"2.6.3", "3.0.3"},
	{"2.6.4", "3.0.3"},
	{"2.6.5", "3.0.3"},
	{"2.6.6", "3.0.3"},
	{"2.6.7", "3.0.3"},
	{"2.6.8", "3.0.3"},
	{"2.6.9", "3.0.3"},
	{"2.6.10", "3.0.3.1"},
	// Ruby 2.7.x
	{"2.7.0", "3.1.2"},
	{"2.7.1", "3.1.2"},
	{"2.7.2", "3.1.4"},
	{"2.7.3", "3.1.6"},
	{"2.7.4", "3.1.6"},
	{"2.7.5", "3.1.6"},
	{"2.7.6", "3.1.6"},
	{"2.7.7", "3.1.6"},
	{"2.7.8", "3.1.6"},
	// Ruby 3.0.x
	{"3.0.0", "3.2.3"},
	{"3.0.1", "3.2.15"},
	{"3.0.2", "3.2.22"},
	{"3.0.3", "3.2.32"},
	{"3.0.4", "3.2.33"},
	{"3.0.5", "3.2.33"},
	{"3.0.6", "3.2.33"},
	{"3.0.7", "3.2.33"},
	// Ruby 3.1.x
	{"3.1.0", "3.3.3"},
	{"3.1.1", "3.3.7"},
	{"3.1.2", "3.3.7"},
	{"3.1.3", "3.3.26"},
	{"3.1.4", "3.3.26"},
	{"3.1.5", "3.3.27"},
	{"3.1.6", "3.3.27"},
	{"3.1.7", "3.3.27"},
	// Ruby 3.2.x
	{"3.2.0", "3.4.1"},
	{"3.2.1", "3.4.6"},
	{"3.2.2", "3.4.10"},
	{"3.2.3", "3.4.19"},
	{"3.2.4", "3.4.19"},
	{"3.2.5", "3.4.19"},
	{"3.2.6", "3.4.19"},
	{"3.2.7", "3.4.19"},
	{"3.2.8", "3.4.19"},
	{"3.2.9", "3.4.19"},
	{"3.2.10", "3.4.19"},
	{"3.2.11", "3.4.19"},
	// Ruby 3.3.x
	{"3.3.0", "3.5.3"},
	{"3.3.1", "3.5.9"},
	{"3.3.2", "3.5.9"},
	{"3.3.3", "3.5.11"},
	{"3.3.4", "3.5.11"},
	{"3.3.5", "3.5.16"},
	{"3.3.6", "3.5.22"},
	{"3.3.7", "3.5.22"},
	{"3.3.8", "3.5.22"},
	{"3.3.9", "3.5.22"},
	{"3.3.10", "3.5.22"},
	{"3.3.11", "3.5.22"},
	// Ruby 3.4.x
	{"3.4.0", "3.6.2"},
	{"3.4.1", "3.6.2"},
	{"3.4.2", "3.6.2"},
	{"3.4.3", "3.6.7"},
	{"3.4.4", "3.6.7"},
	{"3.4.5", "3.6.9"},
	{"3.4.6", "3.6.9"},
	{"3.4.7", "3.6.9"},
	{"3.4.8", "3.6.9"},
	{"3.4.9", "3.6.9"},
}

// rubygemsMajorMinorToRubyMajorMinor maps the RubyGems major.minor series
// to the corresponding Ruby major.minor series. This is used as a fallback
// when the exact rubygems_version doesn't match any bundled version.
var rubygemsMajorMinorToRubyMajorMinor = map[string]string{
	"2.7": "2.5",
	"3.0": "2.6",
	"3.1": "2.7",
	"3.2": "3.0",
	"3.3": "3.1",
	"3.4": "3.2",
	"3.5": "3.3",
	"3.6": "3.4",
}

// rubyVersionForRubygems returns a Ruby version for the given RubyGems version.
// It first attempts an exact reverse-lookup (finding the earliest Ruby that
// bundles this exact RubyGems version). If no exact match, it falls back to
// the minor-series mapping, returning the latest Ruby patch release in the
// corresponding series.
//
// The exact return value indicates whether the RubyGems version was an exact
// match. When false, the caller should explicitly install the target RubyGems
// version (via gem update --system) since the bundled version won't match.
func rubyVersionForRubygems(rubygemsVersion string) (rubyVersion string, exact bool, err error) {
	// Strip dev/pre suffixes for lookup purposes.
	cleaned := cleanRubygemsVersion(rubygemsVersion)

	// Try exact match: find the earliest Ruby that bundles this RubyGems version.
	for _, r := range rubyReleases {
		if r.RubyGems == cleaned {
			return r.Ruby, true, nil
		}
	}

	// Fallback: use the minor-series mapping and pick the latest Ruby patch.
	rgMajorMinor := majorMinor(cleaned)
	rubyMajorMinor, ok := rubygemsMajorMinorToRubyMajorMinor[rgMajorMinor]
	if !ok {
		return "", false, errors.Errorf("unknown RubyGems series %q", rgMajorMinor)
	}

	// Find the latest Ruby in this series.
	var latest string
	for _, r := range rubyReleases {
		if majorMinor(r.Ruby) == rubyMajorMinor {
			latest = r.Ruby
		}
	}
	if latest == "" {
		return "", false, errors.Errorf("no Ruby version found for series %s", rubyMajorMinor)
	}
	return latest, false, nil
}

// rubygemsVersionForRuby returns the RubyGems version bundled with the
// given Ruby version, or "" if unknown.
func rubygemsVersionForRuby(rubyVersion string) string {
	for _, r := range rubyReleases {
		if r.Ruby == rubyVersion {
			return r.RubyGems
		}
	}
	return ""
}

// cleanRubygemsVersion strips dev/pre-release suffixes from a RubyGems version.
// For example, "3.6.0.dev" becomes "3.6.0", "3.5.0.pre1" becomes "3.5.0".
func cleanRubygemsVersion(v string) string {
	for _, suffix := range []string{".dev", ".pre"} {
		if idx := strings.Index(v, suffix); idx != -1 {
			return v[:idx]
		}
	}
	return v
}

// majorMinor extracts the "major.minor" prefix from a version string.
// For example, "3.5.22" returns "3.5".
func majorMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}
