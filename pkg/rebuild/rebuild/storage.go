// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rebuild

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	gcs "cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
)

// AssetType is the type of an asset we're storing for debug later.
type AssetType string

const (
	// DebugRebuildAsset is the artifact that we rebuilt.
	DebugRebuildAsset AssetType = "rebuild"
	// DebugUpstreamAsset is the upstream artifact we compared against.
	DebugUpstreamAsset AssetType = "upstream"
	// DebugLogsAsset is the log we collected.
	DebugLogsAsset AssetType = "logs"

	// RebuildAsset is the artifact associated with the Target.
	RebuildAsset AssetType = "<artifact>"
	// DockerfileAsset is the Dockerfile used to create the builder.
	DockerfileAsset AssetType = "Dockerfile"
	// BuildInfoAsset is the serialized BuildInfo summarizing the remote rebuild.
	BuildInfoAsset AssetType = "info.json"
	// ContainerImageAsset is the container state after executing the rebuild.
	ContainerImageAsset AssetType = "image.tgz"

	// AttestationBundleAsset is the signed attestation bundle generated for a rebuild.
	AttestationBundleAsset AssetType = "rebuild.intoto.jsonl"
)

var (
	// ErrNoUploadPath is an error indicating that no upload path was provided and a DebugStorer couldn't be constructed.
	ErrNoUploadPath = errors.New("no artifact upload path provided")
	// ErrAssetNotFound indicates the asset requested to be read could not be found.
	ErrAssetNotFound = errors.New("asset not found")
)

// Asset represents one of many side effects of rebuilding a single artifact.
//
// Examples include the upstream artifact, the rebuilt artifact, or the logs.
type Asset struct {
	Type   AssetType
	Target Target
}

// AssetStore is a storage mechanism for debug assets.
type AssetStore interface {
	Reader(ctx context.Context, a Asset) (io.ReadCloser, string, error)
	Writer(ctx context.Context, a Asset) (io.WriteCloser, string, error)
}

// AssetCopy copies an asset from one store to another and returns the URI of the destination.
func AssetCopy(ctx context.Context, to, from AssetStore, a Asset) (string, error) {
	r, _, err := from.Reader(ctx, a)
	if err != nil {
		return "", errors.Wrap(err, "from.Reader failed")
	}
	defer r.Close()
	w, uri, err := to.Writer(ctx, a)
	if err != nil {
		return "", errors.Wrap(err, "to.Writer failed")
	}
	defer w.Close()
	if _, err := io.Copy(w, r); err != nil {
		return "", errors.Wrap(err, "copy failed")
	}
	return uri, nil
}

// DebugStoreFromContext constructs a DebugStorer using values from the given context.
func DebugStoreFromContext(ctx context.Context) (AssetStore, error) {
	if uploadpath, ok := ctx.Value(UploadArtifactsPathID).(string); ok {
		if strings.HasPrefix(uploadpath, "gs://") {
			storer, err := NewGCSStore(ctx, uploadpath)
			return storer, errors.Wrapf(err, "Failed to create GCS storer")
		}
		return nil, errors.New("unsupported upload path")
	}
	return nil, ErrNoUploadPath
}

// GCSStore is a debug asset storage backed by GCS.
type GCSStore struct {
	gcsClient *gcs.Client
	bucket    string
	prefix    string
	runID     string
}

// NewGCSStore creates a new GCSStore.
func NewGCSStore(ctx context.Context, uploadPrefix string) (*GCSStore, error) {
	s := &GCSStore{}
	{
		var err error
		gcsOpts := ctx.Value(GCSClientOptionsID).([]option.ClientOption)
		s.gcsClient, err = gcs.NewClient(ctx, gcsOpts...)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to create GCS client")
		}
	}
	s.bucket, s.prefix, _ = strings.Cut(strings.TrimPrefix(uploadPrefix, "gs://"), string(filepath.Separator))
	{
		var ok bool
		s.runID, ok = ctx.Value(RunID).(string)
		if !ok {
			return nil, errors.New("no run ID provided")
		}
	}
	return s, nil
}

func (s *GCSStore) resourcePath(a Asset) string {
	name := string(a.Type)
	if a.Type == RebuildAsset {
		name = a.Target.Artifact
	}
	return filepath.Join(s.prefix, string(a.Target.Ecosystem), a.Target.Package, a.Target.Version, a.Target.Artifact, s.runID, name)
}

// Reader returns a reader for the given asset.
func (s *GCSStore) Reader(ctx context.Context, a Asset) (r io.ReadCloser, uri string, err error) {
	path := s.resourcePath(a)
	obj := s.gcsClient.Bucket(s.bucket).Object(path)
	r, err = obj.NewReader(ctx)
	if err != nil {
		if err == gcs.ErrObjectNotExist {
			err = stderrors.Join(err, ErrAssetNotFound)
		}
		return nil, "", errors.Wrapf(err, "creating GCS reader for %s", path)
	}
	return r, fmt.Sprintf("gs://%s/%s", s.bucket, obj.ObjectName()), nil
}

// Writer returns a writer for the given asset.
func (s *GCSStore) Writer(ctx context.Context, a Asset) (r io.WriteCloser, uri string, err error) {
	objectPath := s.resourcePath(a)
	obj := s.gcsClient.Bucket(s.bucket).Object(objectPath)
	w := obj.NewWriter(ctx)
	return w, fmt.Sprintf("gs://%s/%s", s.bucket, obj.ObjectName()), nil
}

var _ AssetStore = &GCSStore{}

// FilesystemAssetStore will store assets in a billy.Filesystem
type FilesystemAssetStore struct {
	fs billy.Filesystem
}

// TODO: Maybe this should include a runID?
func (s *FilesystemAssetStore) resourcePath(a Asset) string {
	name := string(a.Type)
	if a.Type == RebuildAsset {
		name = a.Target.Artifact
	}
	return filepath.Join(string(a.Target.Ecosystem), a.Target.Package, a.Target.Version, a.Target.Artifact, name)
}

// Reader returns a reader for the given asset.
func (s *FilesystemAssetStore) Reader(ctx context.Context, a Asset) (r io.ReadCloser, uri string, err error) {
	path := s.resourcePath(a)
	f, err := s.fs.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = stderrors.Join(err, ErrAssetNotFound)
		}
		return nil, "", errors.Wrapf(err, "creating reader for %v", a)
	}
	return f, filepath.Join(s.fs.Root(), path), nil
}

// Writer returns a writer for the given asset.
func (s *FilesystemAssetStore) Writer(ctx context.Context, a Asset) (r io.WriteCloser, uri string, err error) {
	path := s.resourcePath(a)
	f, err := s.fs.Create(path)
	if err != nil {
		return nil, "", errors.Wrapf(err, "creating writer for %v", a)
	}
	return f, filepath.Join(s.fs.Root(), path), nil
}

var _ AssetStore = &FilesystemAssetStore{}

// NewFilesystemAssetStore creates a new FilesystemAssetStore.
func NewFilesystemAssetStore(fs billy.Filesystem) *FilesystemAssetStore {
	return &FilesystemAssetStore{fs: fs}
}
