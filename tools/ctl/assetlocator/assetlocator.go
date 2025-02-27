// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package assetlocator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type MetaAssetStore struct {
	MetadataBucket string
	LogsBucket     string
	DebugStorage   string
	Mux            rebuild.RegistryMux
}

func (m *MetaAssetStore) For(ctx context.Context, runID string, wasSmoketest bool) rebuild.ReadOnlyAssetStore {
	return &assetStore{
		metaAssetStore: m,
		runID:          runID,
		wasSmoketest:   wasSmoketest,
	}
}

type assetStore struct {
	metaAssetStore *MetaAssetStore
	runID          string
	wasSmoketest   bool
}

var _ rebuild.ReadOnlyAssetStore = (*assetStore)(nil)

func (m *assetStore) Reader(ctx context.Context, a rebuild.Asset) (io.ReadCloser, error) {
	debug, err := rebuild.DebugStoreFromContext(context.WithValue(context.WithValue(ctx, rebuild.RunID, m.runID), rebuild.DebugStoreID, m.metaAssetStore.DebugStorage))
	if err != nil {
		return nil, errors.Wrap(err, "creating debug asset store")
	}

	switch a.Type {
	case rebuild.DebugUpstreamAsset:
		if m.wasSmoketest {
			return debug.Reader(ctx, a)
		}
		// NOTE: RebuildRemote doesn't store the upstream, so we have to re-download it.
		// If RebuildRemote stored the upstream in the debug bucket, this wouldn't be necessary.
		return rebuild.UpstreamArtifactReader(ctx, a.Target, m.metaAssetStore.Mux)
	case rebuild.RebuildAsset, rebuild.TetragonLogAsset:
		// NOTE: RebuildRemote stores the RebuildAsset and TetragonLogAsset in the metadata bucket.
		// If rebuild remote copied the rebuild artifact into debug, this wouldn't be necessary.
		if m.wasSmoketest {
			return nil, errors.Errorf("asset type not supported during smoketest: %s", a.Type)
		}
		var bi rebuild.BuildInfo
		{
			r, err := debug.Reader(ctx, rebuild.BuildInfoAsset.For(a.Target))
			if err != nil {
				return nil, errors.Wrap(err, "reading build info")
			}
			if json.NewDecoder(r).Decode(&bi) != nil {
				return nil, errors.Wrap(err, "parsing build info")
			}
		}
		metadata, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, bi.ID), fmt.Sprintf("gs://%s", m.metaAssetStore.MetadataBucket))
		if err != nil {
			return nil, errors.Wrap(err, "creating metadata store")
		}
		return metadata.Reader(ctx, a)
	case rebuild.DebugLogsAsset:
		if m.wasSmoketest {
			return debug.Reader(ctx, a)
		}
		// NOTE: Rebuild Remote does not copy the gcb logs, we need to find the gcb
		// build id and then fetch the logs from the gcb logs bucket.
		var bi rebuild.BuildInfo
		{
			r, err := debug.Reader(ctx, rebuild.BuildInfoAsset.For(a.Target))
			if err != nil {
				return nil, errors.Wrap(err, "reading build info")
			}
			if json.NewDecoder(r).Decode(&bi) != nil {
				return nil, errors.Wrap(err, "parsing build info")
			}
		}
		if bi.BuildID == "" {
			return nil, errors.New("BuildID is empty, cannot read gcb logs")
		}
		client, err := gcs.NewClient(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating gcs client")
		}
		obj := client.Bucket(m.metaAssetStore.LogsBucket).Object(gcb.MergedLogFile(bi.BuildID))
		return obj.NewReader(ctx)
	case rebuild.DebugRebuildAsset:
		return debug.Reader(ctx, a)
	default:
		return nil, errors.Errorf("Unsupported asset type: %s", a.Type)
	}
}
