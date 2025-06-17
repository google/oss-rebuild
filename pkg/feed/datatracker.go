// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0
package feed

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

const TrackedPackagesFile = "tracked.json.gz"

// DataSource represents a source of tracked packages data parameterized by version type
type DataSource[T comparable] interface {
	// GetReader returns a reader for the data at the specified version
	GetReader(ctx context.Context, version T) (io.ReadCloser, error)
	// GetCurrentVersion returns the current version of the data
	GetCurrentVersion(ctx context.Context) (T, error)
	// GetIdentifier returns a human-readable identifier for logging
	GetIdentifier() string
}

// VersionedIndex represents a cached index with version information
type VersionedIndex[T comparable] struct {
	Index   TrackedPackageIndex
	Version T
	Ready   chan struct{}
	Err     error
}

var (
	// This in-memory cache makes sure multiple init calls don't need to re-read the index.
	// We use a map keyed by DataSource identifier to support multiple different sources.
	// TODO: Find a way to avoid storing the entire thing in memory, we can probably find a disk-based search index.
	sharedIndexes   = make(map[string]any) // map[string]*VersionedIndex[T]
	sharedIndexesMu sync.Mutex
)

// NewTracker creates a new Tracker by reading a list of tracked packages from the provided data source.
// The data source is expected to contain gzipped JSON: a map from ecosystem (string) to a list of package names (string[]).
func NewTracker[T comparable](ctx context.Context, source DataSource[T], version T) (Tracker, error) {
	sharedIndexesMu.Lock()
	defer sharedIndexesMu.Unlock()

	if sharedIndexes == nil {
		sharedIndexes = make(map[string]any)
	}

	sourceID := source.GetIdentifier()

	// Check if we have a cached index for this source
	var sharedIndex *VersionedIndex[T]
	if cached, exists := sharedIndexes[sourceID]; exists {
		if typedIndex, ok := cached.(*VersionedIndex[T]); ok {
			sharedIndex = typedIndex
		}
	}

	// If version is zero value, get the current version
	var zeroT T
	if version == zeroT {
		var err error
		version, err = source.GetCurrentVersion(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "getting current version")
		}
	}

	// If we don't yet have the in-memory cache, or if version changed, read the cache from the data source.
	if sharedIndex == nil || version != sharedIndex.Version {
		ready := make(chan struct{})
		sharedIndex = &VersionedIndex[T]{Version: version, Ready: ready}
		sharedIndexes[sourceID] = sharedIndex

		go func() {
			defer close(ready)

			log.Printf("Fetching tracked packages list from %s version %v...\n", source.GetIdentifier(), version)

			if err := loadIndex(ctx, source, version); err != nil {
				sharedIndex.Err = err
				return
			}
		}()
	}

	return TrackerFromFunc(func(e schema.TargetEvent) (bool, error) {
		<-sharedIndex.Ready
		if sharedIndex.Err != nil {
			return false, sharedIndex.Err
		}

		if version != sharedIndex.Version {
			log.Printf("Warning: the index version has changed since initialization (expected %v, got %v)",
				version, sharedIndex.Version)
		}

		if _, ok := sharedIndex.Index[e.Ecosystem]; !ok {
			return false, nil
		}

		tracked, ok := sharedIndex.Index[e.Ecosystem][e.Package]
		return ok && tracked, nil
	}), nil
}

// loadIndex loads the tracked packages index from the data source
func loadIndex[T comparable](ctx context.Context, source DataSource[T], version T) error {
	sourceID := source.GetIdentifier()
	sharedIndex := sharedIndexes[sourceID].(*VersionedIndex[T])

	r, err := source.GetReader(ctx, version)
	if err != nil {
		return errors.Wrap(err, "opening data source reader")
	}
	defer logFailure(r.Close)

	gzr, err := gzip.NewReader(r)
	if err != nil {
		return errors.Wrap(err, "opening gzip reader")
	}
	defer logFailure(gzr.Close)

	var rawTracked TrackedPackageSet
	if err := json.NewDecoder(gzr).Decode(&rawTracked); err != nil {
		return errors.Wrap(err, "unmarshalling tracked packages file")
	}

	trackedPackages := make(TrackedPackageIndex)
	var totalPackages int
	for eco, pkgs := range rawTracked {
		if _, ok := trackedPackages[eco]; !ok {
			trackedPackages[eco] = make(map[string]bool)
		}
		for _, pkgName := range pkgs {
			trackedPackages[eco][pkgName] = true
		}
		totalPackages += len(pkgs)
	}

	log.Printf("Loaded index of %d tracked packages\n", totalPackages)
	sharedIndex.Index = trackedPackages
	return nil
}

// GCSObjectDataSource implements DataSource for Google Cloud Storage using int64 generations
type GCSObjectDataSource struct {
	obj *gcs.ObjectHandle
}

// NewGCSObjectDataSource creates a new GCS data source
func NewGCSObjectDataSource(obj *gcs.ObjectHandle) *GCSObjectDataSource {
	return &GCSObjectDataSource{obj: obj}
}

// GetReader implements DataSource.GetReader
func (g *GCSObjectDataSource) GetReader(ctx context.Context, generation int64) (io.ReadCloser, error) {
	return g.obj.Generation(generation).NewReader(ctx)
}

// GetCurrentVersion implements DataSource.GetCurrentVersion
func (g *GCSObjectDataSource) GetCurrentVersion(ctx context.Context) (int64, error) {
	attrs, err := g.obj.Attrs(ctx)
	if err != nil {
		return 0, err
	}
	return attrs.Generation, nil
}

// GetIdentifier implements DataSource.GetIdentifier
func (g *GCSObjectDataSource) GetIdentifier() string {
	return fmt.Sprintf("gs://%s/%s", g.obj.BucketName(), g.obj.ObjectName())
}

// NewGCSTracker creates a new Tracker by reading from a GCS object.
// If generation is 0, the current generation will be fetched.
func NewGCSTracker(ctx context.Context, obj *gcs.ObjectHandle, generation int64) (Tracker, error) {
	source := NewGCSObjectDataSource(obj)
	return NewTracker(ctx, source, generation)
}

func ReadTrackedIndex[T comparable](ctx context.Context, obj DataSource[T], ver T) (TrackedPackageIndex, error) {
	_, err := NewTracker(ctx, obj, ver)
	if err != nil {
		return nil, err
	}
	<-sharedIndexes[obj.GetIdentifier()].(*VersionedIndex[T]).Ready
	if sharedIndexes[obj.GetIdentifier()].(*VersionedIndex[T]).Err != nil {
		return nil, sharedIndexes[obj.GetIdentifier()].(*VersionedIndex[T]).Err
	}
	return sharedIndexes[obj.GetIdentifier()].(*VersionedIndex[T]).Index, nil
}

func logFailure(f func() error) {
	if err := f(); err != nil {
		log.Println(err)
	}
}
