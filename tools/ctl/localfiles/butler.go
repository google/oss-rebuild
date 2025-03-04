// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package localfiles

import (
	"context"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/pkg/errors"
)

// Butler delivers you any assets you desire from the remote assets stores.
type Butler interface {
	Fetch(ctx context.Context, runID string, wasSmoketest bool, want rebuild.Asset) (path string, err error)
}

type butler struct {
	metaAssetstore assetlocator.MetaAssetStore
}

func NewButler(metadataBucket, logsBucket, debugBucket string, mux rebuild.RegistryMux) Butler {
	return &butler{
		metaAssetstore: assetlocator.MetaAssetStore{
			MetadataBucket: metadataBucket,
			LogsBucket:     logsBucket,
			DebugStorage:   debugBucket,
			Mux:            mux,
		},
	}
}

func (b *butler) Fetch(ctx context.Context, runID string, wasSmoketest bool, want rebuild.Asset) (path string, err error) {
	localAssets, err := AssetStore(runID)
	if err != nil {
		return "", err
	}
	if _, err := localAssets.Reader(ctx, want); err == nil {
		return localAssets.URL(want).Path, nil
	}
	forRun := b.metaAssetstore.For(ctx, runID, wasSmoketest)
	if err := rebuild.AssetCopy(ctx, localAssets, forRun, want); err != nil {
		return "", errors.Wrap(err, "copying asset to local store")
	}
	return localAssets.URL(want).Path, nil
}
