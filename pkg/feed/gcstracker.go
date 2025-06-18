// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

const TrackedPackagesFile = "tracked.json.gz"

type GCSTracker struct {
	obj             *gcs.ObjectHandle
	localCache      billy.Filesystem
	trackedPackages map[rebuild.Ecosystem]map[string]bool
}

// NewGCSTracker creates a new GCSTracker.
// It implements Tracker by reading a list of tracked packages for a specific GCS object generation.
// The GCS object is expected to contain gzipped JSON: a map from ecosystem (string) to a list of package names (string[]).
// A local cache of the object's content is maintained in the provided billy.Filesystem.
func NewGCSTracker(ctx context.Context, obj *gcs.ObjectHandle, fs billy.Filesystem) (Tracker, error) {
	tracker := &GCSTracker{obj: obj, localCache: fs}
	attrs, err := tracker.obj.Attrs(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "getting GCS object attributes")
	}
	info, err := tracker.localCache.Stat(TrackedPackagesFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.Wrap(err, "stating local cache file")
	}
	if os.IsNotExist(err) || info.ModTime().Before(attrs.Updated) {
		if err := tracker.fetchFromGCS(ctx); err != nil {
			return nil, errors.Wrap(err, "fetching initial tracked packages list from GCS")
		}
	}
	if err := tracker.readCache(); err != nil {
		return nil, errors.Wrap(err, "reading initial tracked packages from cache")
	}
	return tracker, nil
}

func logFailure(f func() error) {
	if err := f(); err != nil {
		log.Println(err)
	}
}

func (tracker *GCSTracker) fetchFromGCS(ctx context.Context) error {
	log.Printf("Fetching tracked packages list from GCS for gs://%s/%s generation %d...", tracker.obj.BucketName(), tracker.obj.ObjectName(), tracker.obj.Generation())
	r, err := tracker.obj.NewReader(ctx)
	if err != nil {
		return errors.Wrap(err, "opening GCS reader")
	}
	defer logFailure(r.Close)
	w, err := tracker.localCache.Create(TrackedPackagesFile)
	if err != nil {
		return errors.Wrap(err, "opening cache writer")
	}
	defer logFailure(w.Close)
	if _, err := io.Copy(w, r); err != nil {
		return errors.Wrap(err, "writing tracked list locally")
	}
	return nil
}

func (tracker *GCSTracker) readCache() error {
	r, err := tracker.localCache.Open(TrackedPackagesFile)
	if err != nil {
		return errors.Wrap(err, "opening cache file")
	}
	defer logFailure(r.Close)
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return errors.Wrap(err, "opening zip reader")
	}
	defer logFailure(gzr.Close)
	var rawTracked TrackedPackageSet
	if err := json.NewDecoder(gzr).Decode(&rawTracked); err != nil {
		return errors.Wrapf(err, "unmarshalling tracked packages file: %s", tracker.localCache)
	}
	trackedPackages := make(TrackedPackageIndex)
	for eco, pkgs := range rawTracked {
		if _, ok := trackedPackages[eco]; !ok {
			trackedPackages[eco] = make(map[string]bool)
		}
		for _, pkgName := range pkgs {
			trackedPackages[eco][pkgName] = true
		}
	}
	tracker.trackedPackages = trackedPackages
	return nil
}

// IsTracked checks if the given TargetEvent corresponds to a tracked package.
func (lft *GCSTracker) IsTracked(e schema.TargetEvent) (bool, error) {
	// Tracked packages are loaded at construction time via NewGCSTracker.
	if lft.trackedPackages == nil {
		return false, errors.New("GCSTracker's trackedPackages map is nil, indicating an initialization issue")
	}
	if ecoMap, ok := lft.trackedPackages[e.Ecosystem]; ok {
		if _, tracked := ecoMap[e.Package]; tracked {
			return true, nil
		}
	}
	return false, nil
}

var _ Tracker = &GCSTracker{}
