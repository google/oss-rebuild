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

func (m *MetaAssetStore) For(ctx context.Context, runID string) (rebuild.ReadOnlyAssetStore, error) {
	debug, err := rebuild.DebugStoreFromContext(context.WithValue(context.WithValue(ctx, rebuild.RunID, runID), rebuild.DebugStoreID, m.DebugStorage))
	if err != nil {
		return nil, errors.Wrap(err, "creating debug asset store")
	}
	return &assetStore{
		metaAssetStore: m,
		debug:          debug,
	}, nil
}

type assetStore struct {
	metaAssetStore *MetaAssetStore
	debug          rebuild.AssetStore
}

var _ rebuild.ReadOnlyAssetStore = (*assetStore)(nil)

func (m *assetStore) buildInfo(ctx context.Context, t rebuild.Target) (*rebuild.BuildInfo, error) {
	r, err := m.debug.Reader(ctx, rebuild.BuildInfoAsset.For(t))
	if err != nil {
		return nil, errors.Wrap(err, "reading build info")
	}
	bi := new(rebuild.BuildInfo)
	if json.NewDecoder(r).Decode(bi) != nil {
		return nil, errors.Wrap(err, "parsing build info")
	}
	return bi, nil
}

func (m *assetStore) Reader(ctx context.Context, a rebuild.Asset) (io.ReadCloser, error) {
	// Convert requests to the correct type of rebuild asset
	switch a.Type {
	case rebuild.DockerfileAsset, rebuild.BuildInfoAsset:
		return m.debug.Reader(ctx, a)
	case rebuild.DebugUpstreamAsset:
		// NOTE: RebuildRemote doesn't store the upstream, so we have to re-download it.
		// If RebuildRemote stored the upstream in the debug bucket, this wouldn't be necessary.
		return rebuild.UpstreamArtifactReader(ctx, a.Target, m.metaAssetStore.Mux)
	case rebuild.RebuildAsset, rebuild.TetragonLogAsset:
		// NOTE: RebuildRemote stores the RebuildAsset and TetragonLogAsset in the metadata bucket.
		// If rebuild remote copied the rebuild artifact into debug, this wouldn't be necessary.
		bi, err := m.buildInfo(ctx, a.Target)
		if err != nil {
			return nil, err
		}
		metadata, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, bi.ObliviousID), fmt.Sprintf("gs://%s", m.metaAssetStore.MetadataBucket))
		if err != nil {
			return nil, errors.Wrap(err, "creating metadata store")
		}
		return metadata.Reader(ctx, a)
	case rebuild.DebugLogsAsset:
		// NOTE: Rebuild Remote does not copy the gcb logs, we need to find the gcb
		// build id and then fetch the logs from the gcb logs bucket.
		bi, err := m.buildInfo(ctx, a.Target)
		if err != nil {
			return nil, err
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
	default:
		return nil, errors.Errorf("Unsupported asset type: %s", a.Type)
	}
}
