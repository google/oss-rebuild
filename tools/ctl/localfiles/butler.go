// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package localfiles

import (
	"context"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/pkg/errors"
)

// Butler delivers you any assets you desire from the remote assets stores.
type Butler interface {
	Fetch(ctx context.Context, runID string, wasSmoketest bool, want rebuild.Asset) (path string, err error)
}

type butler struct {
	metaAssetstore assetlocator.MetaAssetStore
	assetStoreFn   func(runID string) (rebuild.LocatableAssetStore, error)
}

func NewButler(metadataBucket, logsBucket, debugBucket string, mux rebuild.RegistryMux, assetStoreFn func(runID string) (rebuild.LocatableAssetStore, error)) Butler {
	return &butler{
		metaAssetstore: assetlocator.MetaAssetStore{
			MetadataBucket: metadataBucket,
			LogsBucket:     logsBucket,
			DebugStorage:   debugBucket,
			Mux:            mux,
		},
		assetStoreFn: assetStoreFn,
	}
}

func (b *butler) Fetch(ctx context.Context, runID string, wasSmoketest bool, want rebuild.Asset) (path string, err error) {
	dest, err := b.assetStoreFn(runID)
	if err != nil {
		return "", err
	}
	if r, err := dest.Reader(ctx, want); err == nil {
		if err := r.Close(); err != nil {
			return "", err
		}
		return dest.URL(want).Path, nil
	}
	switch want.Type {
	case diffoscope.DiffAsset:
		var rba, usa string
		{
			rba, err = b.Fetch(ctx, runID, wasSmoketest, rebuild.RebuildAsset.For(want.Target))
			if err != nil {
				return "", errors.Wrap(err, "fetching rebuild asset")
			}
			usa, err = b.Fetch(ctx, runID, wasSmoketest, rebuild.DebugUpstreamAsset.For(want.Target))
			if err != nil {
				return "", errors.Wrap(err, "fetching upstream asset")
			}
		}
		contents, err := diffoscope.DiffArtifacts(ctx, rba, usa, want.Target)
		if err != nil {
			return "", errors.Wrap(err, "executing diff")
		}
		w, err := dest.Writer(ctx, want)
		if err != nil {
			return "", err
		}
		defer w.Close()
		_, err = w.Write([]byte(contents))
		if err != nil {
			return "", err
		}
	default:
		forRun, err := b.metaAssetstore.For(ctx, runID, wasSmoketest)
		if err != nil {
			return "", errors.Wrap(err, "creating asset store")
		}
		if err := rebuild.AssetCopy(ctx, dest, forRun, want); err != nil {
			return "", errors.Wrap(err, "copying asset to local store")
		}
	}
	return dest.URL(want).Path, nil
}
