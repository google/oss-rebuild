// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"sort"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// errNoVersionLister reports that an ecosystem has no way to enumerate published
// versions. Callers should treat it as an expected limitation rather than a
// failure, and must not confuse it with a lookup that errored.
var errNoVersionLister = errors.New("ecosystem has no version lister")

// publishedVersions returns a package's registry-published versions newest-first
// by semver. Ecosystems that cannot enumerate versions return
// errNoVersionLister.
//
// NOTE: The GetVersions helpers order by publish time, which is wrong for a
// version history. A late backport to an old major would sort above the current
// releases. Re-sorting by semver puts them in true version order.
func publishedVersions(ctx context.Context, mux rebuild.RegistryMux, target rebuild.Target) ([]string, error) {
	var versions []string
	var err error
	switch target.Ecosystem {
	case rebuild.NPM:
		versions, err = npm.GetVersions(ctx, target.Package, mux)
	case rebuild.CratesIO:
		versions, err = cratesio.GetVersions(ctx, target.Package, mux)
	default:
		return nil, errNoVersionLister
	}
	if err != nil {
		return nil, errors.Wrap(err, "listing versions")
	}
	sortVersionsDesc(versions)
	return versions, nil
}

// sortVersionsDesc orders versions newest-first by semver. Unparseable versions
// sort after all valid ones so a stray non-semver tag can't displace a release.
func sortVersionsDesc(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool {
		vi, ei := semver.New(versions[i])
		vj, ej := semver.New(versions[j])
		switch {
		case ei == nil && ej == nil:
			return vi.Compare(vj) > 0
		case ei == nil:
			return true
		case ej == nil:
			return false
		default:
			return versions[i] > versions[j]
		}
	})
}
