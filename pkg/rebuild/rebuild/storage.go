// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	stderrors "errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
)

// AssetType is the type of an asset we're storing for debug later.
type AssetType string

func (a AssetType) For(t Target) Asset {
	return Asset{Type: a, Target: t}
}

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
	// ProxyNetlogAsset is the network activity from the rebuild process.
	ProxyNetlogAsset AssetType = "netlog.json"
	// TetragonLogAsset is the log of all tetragon events.
	TetragonLogAsset AssetType = "tetragon.jsonl"

	// AttestationBundleAsset is the signed attestation bundle generated for a rebuild.
	AttestationBundleAsset AssetType = "rebuild.intoto.jsonl"

	// BuildDef is the build definition, including strategy.
	BuildDef AssetType = "build.yaml"
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

// assetPath describes the general layout of assets shared by most hierarchy-based AssetStore types. The runID
func assetPath(a Asset, runID string) []string {
	name := string(a.Type)
	if a.Type == RebuildAsset {
		name = a.Target.Artifact
	}
	return []string{string(a.Target.Ecosystem), a.Target.Package, a.Target.Version, a.Target.Artifact, runID, name}
}

// ReadOnlyAssetStore is a storage mechanism for debug assets.
type ReadOnlyAssetStore interface {
	Reader(ctx context.Context, a Asset) (io.ReadCloser, error)
}

// AssetStore is a storage mechanism for debug assets.
type AssetStore interface {
	ReadOnlyAssetStore
	Writer(ctx context.Context, a Asset) (io.WriteCloser, error)
}

// LocatableAssetStore is an asset store whose assets can be identified with a URL.
type LocatableAssetStore interface {
	AssetStore
	URL(a Asset) *url.URL
}

// AssetCopy copies an asset from one store to another.
func AssetCopy(ctx context.Context, to AssetStore, from ReadOnlyAssetStore, a Asset) error {
	r, err := from.Reader(ctx, a)
	if err != nil {
		return errors.Wrap(err, "from.Reader failed")
	}
	defer r.Close()
	w, err := to.Writer(ctx, a)
	if err != nil {
		return errors.Wrap(err, "to.Writer failed")
	}
	defer w.Close()
	if _, err := io.Copy(w, r); err != nil {
		return errors.Wrap(err, "copy failed")
	}
	return w.Close()
}

// DebugStoreFromContext constructs a DebugStorer using values from the given context.
func DebugStoreFromContext(ctx context.Context) (AssetStore, error) {
	if uploadpath, ok := ctx.Value(DebugStoreID).(string); ok {
		if uploadpath == "" {
			return nil, ErrNoUploadPath
		}
		u, err := url.Parse(uploadpath)
		if err != nil {
			return nil, errors.Wrap(err, "parsing as url")
		}
		switch u.Scheme {
		case "gs":
			storer, err := NewGCSStore(ctx, uploadpath)
			return storer, errors.Wrapf(err, "creating GCS storer")
		case "file":
			path := u.Path
			if runID, ok := ctx.Value(RunID).(string); ok {
				path = filepath.Join(path, runID)
			}
			os.MkdirAll(path, 0755)
			return NewFilesystemAssetStore(osfs.New(path)), nil
		default:
			return nil, errors.Errorf("unsupported scheme: '%s'", u.Scheme)
		}
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
		var gcsOpts []option.ClientOption
		if opts, ok := ctx.Value(GCSClientOptionsID).([]option.ClientOption); ok {
			gcsOpts = append(gcsOpts, opts...)
		}
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

func (s *GCSStore) URL(a Asset) *url.URL {
	return &url.URL{Scheme: "gs", Path: filepath.Join(s.bucket, s.resourcePath(a))}
}

func (s *GCSStore) resourcePath(a Asset) string {
	return path.Join(append([]string{s.prefix}, assetPath(a, s.runID)...)...)
}

// Reader returns a reader for the given asset.
func (s *GCSStore) Reader(ctx context.Context, a Asset) (io.ReadCloser, error) {
	path := s.resourcePath(a)
	obj := s.gcsClient.Bucket(s.bucket).Object(path)
	r, err := obj.NewReader(ctx)
	if err != nil {
		if err == gcs.ErrObjectNotExist {
			err = stderrors.Join(err, ErrAssetNotFound)
		}
		return nil, errors.Wrapf(err, "creating GCS reader for %s", path)
	}
	return r, nil
}

// Writer returns a writer for the given asset.
func (s *GCSStore) Writer(ctx context.Context, a Asset) (io.WriteCloser, error) {
	objectPath := s.resourcePath(a)
	obj := s.gcsClient.Bucket(s.bucket).Object(objectPath)
	w := obj.NewWriter(ctx)
	return w, nil
}

var _ LocatableAssetStore = &GCSStore{}

// FilesystemAssetStore will store assets in a billy.Filesystem
type FilesystemAssetStore struct {
	fs    billy.Filesystem
	runID string
}

func (s *FilesystemAssetStore) resourcePath(a Asset) string {
	return filepath.Join(assetPath(a, s.runID)...)
}

func (s *FilesystemAssetStore) URL(a Asset) *url.URL {
	return &url.URL{Scheme: "file", Path: filepath.Join(s.fs.Root(), s.resourcePath(a))}
}

// Reader returns a reader for the given asset.
func (s *FilesystemAssetStore) Reader(ctx context.Context, a Asset) (io.ReadCloser, error) {
	path := s.resourcePath(a)
	f, err := s.fs.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = stderrors.Join(err, ErrAssetNotFound)
		}
		return nil, errors.Wrapf(err, "creating reader for %v", a)
	}
	return f, nil
}

// Writer returns a writer for the given asset.
func (s *FilesystemAssetStore) Writer(ctx context.Context, a Asset) (io.WriteCloser, error) {
	path := s.resourcePath(a)
	f, err := s.fs.Create(path)
	if err != nil {
		return nil, errors.Wrapf(err, "creating writer for %v", a)
	}
	return f, nil
}

var _ LocatableAssetStore = &FilesystemAssetStore{}

// NewFilesystemAssetStoreWithRunID creates a new FilesystemAssetStore.
func NewFilesystemAssetStoreWithRunID(fs billy.Filesystem, runID string) *FilesystemAssetStore {
	return &FilesystemAssetStore{fs: fs, runID: runID}
}

// NewFilesystemAssetStore creates a new FilesystemAssetStore.
func NewFilesystemAssetStore(fs billy.Filesystem) *FilesystemAssetStore {
	return NewFilesystemAssetStoreWithRunID(fs, "")
}

// CachedAssetStore implements a pullthrough cache with backline only being read if the asset isn't present in frontline.
type CachedAssetStore struct {
	frontline LocatableAssetStore
	backline  AssetStore
}

func NewCachedAssetStore(frontline LocatableAssetStore, backline AssetStore) *CachedAssetStore {
	return &CachedAssetStore{frontline: frontline, backline: backline}
}

type cachedReader struct {
	tee        io.Reader
	writeClose func() error
	readClose  func() error
}

func (cr *cachedReader) Read(p []byte) (int, error) {
	return cr.tee.Read(p)
}

func (cr *cachedReader) Close() error {
	// Read the remaining tee, otherwise the frontline might be only partially populated.
	_, flushErr := io.ReadAll(cr.tee)
	writeErr := cr.writeClose()
	readErr := cr.readClose()
	if flushErr != nil && flushErr != io.EOF {
		return flushErr
	}
	if writeErr != nil {
		return writeErr
	}
	return readErr
}

// Reader reads from the frontline, unless the frontline returns ErrAssetNotFound
func (s *CachedAssetStore) Reader(ctx context.Context, a Asset) (io.ReadCloser, error) {
	if r, err := s.frontline.Reader(ctx, a); !errors.Is(err, ErrAssetNotFound) {
		return r, err
	}
	// Cache miss, fetch from the backline
	br, err := s.backline.Reader(ctx, a)
	if err != nil {
		return nil, err
	}
	fw, err := s.frontline.Writer(ctx, a)
	if err != nil {
		return nil, err
	}
	return &cachedReader{
		tee:        io.TeeReader(br, fw),
		writeClose: fw.Close,
		readClose:  br.Close,
	}, nil
}

type multiWriterCloser struct {
	w        io.Writer
	closeFns []func() error
}

func (mwc *multiWriterCloser) Write(p []byte) (int, error) {
	return mwc.w.Write(p)
}

func (mwc *multiWriterCloser) Close() error {
	var errs []error
	for _, closer := range mwc.closeFns {
		errs = append(errs, closer())
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Wrtier writes to both the frontline and the backline of the cache.
func (s *CachedAssetStore) Writer(ctx context.Context, a Asset) (io.WriteCloser, error) {
	fw, err := s.frontline.Writer(ctx, a)
	if err != nil {
		return nil, err
	}
	bw, err := s.backline.Writer(ctx, a)
	if err != nil {
		return nil, err
	}
	return &multiWriterCloser{io.MultiWriter(fw, bw), []func() error{fw.Close, bw.Close}}, nil
}

func (s *CachedAssetStore) URL(a Asset) *url.URL {
	return s.frontline.URL(a)
}
