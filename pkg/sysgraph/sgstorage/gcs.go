// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// GetGCSClient returns a GCS client.
// TODO: Refactor to accept a context instead of using context.Background().
var GetGCSClient = sync.OnceValues(func() (*storage.Client, error) {
	return storage.NewGRPCClient(context.Background())
})

func newGCSFS(path string) (*gcsFS, error) {
	bucket, object, err := parseGCSPath(path)
	if err != nil {
		return nil, err
	}
	client, err := GetGCSClient()
	if err != nil {
		return nil, err
	}
	return &gcsFS{bucket: client.Bucket(bucket), path: object}, nil
}

type gcsFS struct {
	bucket *storage.BucketHandle
	path   string
}

var _ ctxFS = (*gcsFS)(nil)
var _ writerFS = (*gcsFS)(nil)

// ReadFile reads a file from GCS.
func (g *gcsFS) ReadFile(ctx context.Context, name string) ([]byte, error) {
	obj, err := g.bucket.Object(filepath.Join(g.path, name)).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

// ReadDir returns the list of files in the given directory.
func (g *gcsFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	prefix := filepath.Join(g.path, name)
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	objs := g.bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	var res []fs.DirEntry
	for {
		obj, err := objs.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}

		res = append(res, &gcsFileInfo{obj})
	}
	return res, nil
}

// MkdirAll is a no-op for GCSFileFS.
// GCS does not support directories.
func (g *gcsFS) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	return nil
}

// WriteFile writes a file to GCS.
func (g *gcsFS) WriteFile(ctx context.Context, path string, blob []byte) error {
	writer := g.bucket.Object(path).NewWriter(ctx)
	if _, err := writer.Write(blob); err != nil {
		return err
	}
	return writer.Close()
}

// FileWriter returns a buffered writer for a file in GCS.
func (g *gcsFS) FileWriter(ctx context.Context, path string) (io.WriteCloser, error) {
	writer := g.bucket.Object(path).NewWriter(ctx)
	return &BufferedWriteCloser{bufio.NewWriter(writer), writer}, nil
}

type gcsFileInfo struct {
	attrs *storage.ObjectAttrs
}

var _ fs.FileInfo = (*gcsFileInfo)(nil)
var _ fs.DirEntry = (*gcsFileInfo)(nil)

func (f *gcsFileInfo) Name() string {
	return filepath.Base(f.attrs.Name)
}

func (f *gcsFileInfo) Size() int64 {
	return f.attrs.Size
}

func (f *gcsFileInfo) ModTime() time.Time {
	return f.attrs.Updated
}

func (f *gcsFileInfo) IsDir() bool {
	return f.attrs.Prefix != ""
}

func (f *gcsFileInfo) Mode() fs.FileMode {
	if f.IsDir() {
		return fs.ModeDir
	}
	return 0
}

func (f *gcsFileInfo) Type() fs.FileMode {
	if f.IsDir() {
		return fs.ModeDir
	}
	return 0
}

func (f *gcsFileInfo) Sys() any {
	return nil
}

func (f *gcsFileInfo) Info() (fs.FileInfo, error) {
	return f, nil
}

func parseGCSPath(fpath string) (bucket, object string, err error) {
	if !strings.HasPrefix(fpath, "gs://") {
		return "", "", fmt.Errorf("invalid GCS path: %s", fpath)
	}
	fpath = strings.TrimPrefix(fpath, "gs://")
	parts := strings.SplitN(fpath, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid GCS path: %s", fpath)
	}
	return parts[0], parts[1], nil
}

func readFromGCS(ctx context.Context, fpath string) (*BufferedReadCloser, error) {
	fpath = strings.TrimSuffix(fpath, "/")
	bucket, object, err := parseGCSPath(fpath)
	if err != nil {
		return nil, err
	}
	client, err := GetGCSClient()
	if err != nil {
		return nil, err
	}
	obj, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	return &BufferedReadCloser{bufio.NewReader(obj), obj}, nil
}

func gcsFileWriter(ctx context.Context, fpath string) (*BufferedWriteCloser, error) {
	fpath = strings.TrimSuffix(fpath, "/")
	bucket, object, err := parseGCSPath(fpath)
	if err != nil {
		return nil, err
	}
	client, err := GetGCSClient()
	if err != nil {
		return nil, err
	}
	writer := client.Bucket(bucket).Object(object).NewWriter(ctx)
	return &BufferedWriteCloser{bufio.NewWriter(writer), writer}, nil
}

func copyFromGCS(ctx context.Context, path string) (string, error) {
	gcsZipFile, err := readFromGCS(ctx, path)
	if err != nil {
		return "", err
	}
	defer gcsZipFile.Close()
	localFile, err := os.CreateTemp("", "sgstorage")
	if err != nil {
		return "", err
	}
	defer localFile.Close()
	if _, err := io.Copy(localFile, gcsZipFile); err != nil {
		return "", err
	}
	return localFile.Name(), nil
}
