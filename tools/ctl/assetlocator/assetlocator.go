// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package assetlocator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
)

type MetaAssetStore struct {
	MetadataBucket string
	LogsBucket     string
	DebugStorage   string
	Mux            rebuild.RegistryMux
	gcsClientOnce  sync.Once
	gcsClient      *gcs.Client
	gcsClientErr   error
}

func (m *MetaAssetStore) client(ctx context.Context) (*gcs.Client, error) {
	m.gcsClientOnce.Do(func() {
		var gcsOpts []option.ClientOption
		if opts, ok := ctx.Value(rebuild.GCSClientOptionsID).([]option.ClientOption); ok {
			gcsOpts = append(gcsOpts, opts...)
		}
		m.gcsClient, m.gcsClientErr = gcs.NewClient(context.WithoutCancel(ctx), gcsOpts...)
	})
	return m.gcsClient, m.gcsClientErr
}

func (m *MetaAssetStore) For(ctx context.Context, runID string) (rebuild.ReadOnlyAssetStore, error) {
	var debug rebuild.AssetStore
	var err error
	if strings.HasPrefix(m.DebugStorage, "gs://") {
		client, err := m.client(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating gcs client")
		}
		debug, err = rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, runID), client, m.DebugStorage)
		if err != nil {
			return nil, errors.Wrap(err, "creating debug asset store")
		}
	} else {
		debug, err = rebuild.DebugStoreFromContext(context.WithValue(context.WithValue(ctx, rebuild.RunID, runID), rebuild.DebugStoreID, m.DebugStorage))
		if err != nil {
			return nil, errors.Wrap(err, "creating debug asset store")
		}
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
	defer r.Close()
	bi := new(rebuild.BuildInfo)
	if err := json.NewDecoder(r).Decode(bi); err != nil {
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
		return meta.AllRebuilders[a.Target.Ecosystem].Upstream(ctx, a.Target, m.metaAssetStore.Mux)
	case rebuild.RebuildAsset, rebuild.TetragonLogAsset, rebuild.SysgraphAsset, rebuild.ProxyNetlogAsset:
		// NOTE: RebuildRemote stores the RebuildAsset and TetragonLogAsset in the metadata bucket.
		// If rebuild remote copied the rebuild artifact into debug, this wouldn't be necessary.
		bi, err := m.buildInfo(ctx, a.Target)
		if err != nil {
			return nil, err
		}
		client, err := m.metaAssetStore.client(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating gcs client")
		}
		metadata, err := rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, bi.ObliviousID), client, fmt.Sprintf("gs://%s", m.metaAssetStore.MetadataBucket))
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
		client, err := m.metaAssetStore.client(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating gcs client")
		}
		return gcb.NewGCSLogsClient(client, m.metaAssetStore.LogsBucket).ReadBuildLogs(ctx, bi.BuildID)
	default:
		return nil, errors.Errorf("Unsupported asset type: %s", a.Type)
	}
}
