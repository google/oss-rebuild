// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/pkg/errors"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

// GCS is a billy.Filesystem over one GCS bucket prefix.
// Object storage is not a filesystem but, with some modest restrictions, we
// can fill out the interface: files are readable or writable but never both,
// writers are sequential (no Seek, no Truncate), directories are object
// prefixes (i.e. MkdirAll is a no-op), and there are no temp files, symlinks,
// or locks. Capabilities declares the supported subset. Unfortunately, billy
// has no context plumbing, so all operations run under the context the
// filesystem was constructed with.
type GCS struct {
	ctx    context.Context
	client *storage.Client
	bucket string
	prefix string
}

var _ billy.Filesystem = (*GCS)(nil)
var _ billy.Capable = (*GCS)(nil)

// NewGCS serves bucket under prefix via client.
func NewGCS(ctx context.Context, client *storage.Client, bucket, prefix string) *GCS {
	return &GCS{ctx: ctx, client: client, bucket: bucket, prefix: strings.Trim(prefix, "/")}
}

// Capabilities declares the supported subset: sequential reads and writes.
func (f *GCS) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.WriteCapability
}

// locate resolves a path to its object handle's parts. The filesystem root
// resolves to the bare prefix, empty when none is set.
func (f *GCS) locate(name string) (*storage.BucketHandle, string, error) {
	object := strings.TrimPrefix(path.Clean("/"+name), "/")
	if f.prefix != "" {
		object = path.Join(f.prefix, object)
	}
	return f.client.Bucket(f.bucket), object, nil
}

// hasStatus reports whether err is a googleapi error with the given code.
// Only some client calls map statuses to sentinels like ErrObjectNotExist.
func hasStatus(err error, code int) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == code
}

func (f *GCS) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0)
}

func (f *GCS) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

func (f *GCS) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	bucket, object, err := f.locate(filename)
	if err != nil {
		return nil, err
	}
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		return &readFile{ctx: f.ctx, name: filename, obj: bucket.Object(object)}, nil
	case os.O_WRONLY:
		if flag&os.O_APPEND != 0 {
			return nil, billy.ErrNotSupported
		}
		return &writeFile{name: filename, w: bucket.Object(object).NewWriter(f.ctx)}, nil
	default:
		// A file is readable or writable, never both.
		return nil, billy.ErrNotSupported
	}
}

func (f *GCS) Stat(filename string) (os.FileInfo, error) {
	bucket, object, err := f.locate(filename)
	if err != nil {
		return nil, err
	}
	// The root always reads as a directory: only the empty relative path
	// resolves to the bare prefix, and Attrs rejects an empty name.
	if object == f.prefix {
		return fileInfo{name: path.Base(filename), dir: true}, nil
	}
	attrs, err := bucket.Object(object).Attrs(f.ctx)
	if err == nil {
		return fileInfo{name: path.Base(filename), size: attrs.Size, mtime: attrs.Updated}, nil
	}
	if !errors.Is(err, storage.ErrObjectNotExist) {
		return nil, err
	}
	// A prefix with objects under it reads as a directory.
	it := bucket.Objects(f.ctx, &storage.Query{Prefix: object + "/"})
	switch _, err := it.Next(); err {
	case nil:
		return fileInfo{name: path.Base(filename), dir: true}, nil
	case iterator.Done:
		return nil, &os.PathError{Op: "stat", Path: filename, Err: fs.ErrNotExist}
	default:
		return nil, err
	}
}

// Rename is a server-side copy then delete. No object data transits the
// client, but the pair is not atomic. GCS's native move is restricted to
// hierarchical-namespace buckets, which we cannot assume.
func (f *GCS) Rename(oldpath, newpath string) error {
	obucket, oobject, err := f.locate(oldpath)
	if err != nil {
		return err
	}
	nbucket, nobject, err := f.locate(newpath)
	if err != nil {
		return err
	}
	src, dst := obucket.Object(oobject), nbucket.Object(nobject)
	if _, err := dst.CopierFrom(src).Run(f.ctx); err != nil {
		// NOTE: Rewrite 404s are not currently mapped to ErrObjectNotExist.
		if errors.Is(err, storage.ErrObjectNotExist) || hasStatus(err, http.StatusNotFound) {
			return &os.PathError{Op: "rename", Path: oldpath, Err: fs.ErrNotExist}
		}
		return err
	}
	return src.Delete(f.ctx)
}

func (f *GCS) Remove(filename string) error {
	bucket, object, err := f.locate(filename)
	if err != nil {
		return err
	}
	err = bucket.Object(object).Delete(f.ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return &os.PathError{Op: "remove", Path: filename, Err: fs.ErrNotExist}
	}
	return err
}

func (f *GCS) Join(elem ...string) string { return path.Join(elem...) }

func (f *GCS) ReadDir(dir string) ([]os.FileInfo, error) {
	bucket, object, err := f.locate(dir)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if object != "" {
		prefix = object + "/"
	}
	it := bucket.Objects(f.ctx, &storage.Query{Prefix: prefix, Delimiter: "/"})
	var out []os.FileInfo
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		// Skip the listed directory's own zero-byte placeholder object,
		// as created by the Cloud Console. Nested placeholders are already
		// collapsed into Prefixes by the delimiter.
		if attrs.Name == prefix {
			continue
		}
		if attrs.Prefix != "" {
			out = append(out, fileInfo{name: path.Base(strings.TrimSuffix(attrs.Prefix, "/")), dir: true})
			continue
		}
		out = append(out, fileInfo{name: path.Base(attrs.Name), size: attrs.Size, mtime: attrs.Updated})
	}
}

// MkdirAll is a no-op: directories are prefix fictions.
func (f *GCS) MkdirAll(string, os.FileMode) error { return nil }

func (f *GCS) TempFile(string, string) (billy.File, error) { return nil, billy.ErrNotSupported }

func (f *GCS) Lstat(filename string) (os.FileInfo, error) { return f.Stat(filename) }

func (f *GCS) Symlink(string, string) error { return billy.ErrNotSupported }

func (f *GCS) Readlink(string) (string, error) { return "", billy.ErrNotSupported }

func (f *GCS) Chroot(dir string) (billy.Filesystem, error) {
	p := strings.TrimPrefix(path.Clean("/"+dir), "/")
	return NewGCS(f.ctx, f.client, f.bucket, path.Join(f.prefix, p)), nil
}

func (f *GCS) Root() string { return "gs://" + f.bucket + "/" + f.prefix }

// readFile reads an object, opening the stream lazily so Seek before the
// first Read is free, and reopening a ranged reader after any Seek.
type readFile struct {
	ctx  context.Context
	name string
	obj  *storage.ObjectHandle
	r    *storage.Reader
	pos  int64
}

func (r *readFile) Name() string { return r.name }

func (r *readFile) Read(p []byte) (int, error) {
	if r.r == nil {
		nr, err := r.obj.NewRangeReader(r.ctx, r.pos, -1)
		if errors.Is(err, storage.ErrObjectNotExist) {
			return 0, &os.PathError{Op: "read", Path: r.name, Err: fs.ErrNotExist}
		}
		// A range at or past the object's end is an error, not an empty read.
		if hasStatus(err, http.StatusRequestedRangeNotSatisfiable) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.r = nr
	}
	n, err := r.r.Read(p)
	r.pos += int64(n)
	return n, err
}

func (r *readFile) ReadAt(p []byte, off int64) (int, error) {
	nr, err := r.obj.NewRangeReader(r.ctx, off, int64(len(p)))
	if errors.Is(err, storage.ErrObjectNotExist) {
		return 0, &os.PathError{Op: "read", Path: r.name, Err: fs.ErrNotExist}
	}
	// A range at or past the object's end is an error, not an empty read.
	if hasStatus(err, http.StatusRequestedRangeNotSatisfiable) {
		return 0, io.EOF
	}
	if err != nil {
		return 0, err
	}
	defer nr.Close()
	// GCS truncates the range at the object's end, which ReadFull reports
	// as ErrUnexpectedEOF where the ReaderAt contract expects EOF.
	n, err := io.ReadFull(nr, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err
}

func (r *readFile) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		attrs, err := r.obj.Attrs(r.ctx)
		if err != nil {
			return 0, err
		}
		base = attrs.Size
	default:
		return 0, billy.ErrNotSupported
	}
	pos := base + offset
	if pos < 0 { // negative position means "last N bytes" to GCS
		return 0, &os.PathError{Op: "seek", Path: r.name, Err: fs.ErrInvalid}
	}
	if r.r != nil {
		r.r.Close()
		r.r = nil
	}
	r.pos = pos
	return r.pos, nil
}

func (r *readFile) Close() error {
	if r.r == nil {
		return nil
	}
	return r.r.Close()
}

func (r *readFile) Write([]byte) (int, error) { return 0, billy.ErrNotSupported }
func (r *readFile) Truncate(int64) error      { return billy.ErrNotSupported }
func (r *readFile) Lock() error               { return billy.ErrNotSupported }
func (r *readFile) Unlock() error             { return billy.ErrNotSupported }

// writeFile streams one object write, committed by Close.
type writeFile struct {
	name string
	w    *storage.Writer
}

func (w *writeFile) Name() string                { return w.name }
func (w *writeFile) Write(p []byte) (int, error) { return w.w.Write(p) }
func (w *writeFile) Close() error                { return w.w.Close() }

func (w *writeFile) Read([]byte) (int, error)          { return 0, billy.ErrNotSupported }
func (w *writeFile) ReadAt([]byte, int64) (int, error) { return 0, billy.ErrNotSupported }
func (w *writeFile) Seek(int64, int) (int64, error)    { return 0, billy.ErrNotSupported }
func (w *writeFile) Truncate(int64) error              { return billy.ErrNotSupported }
func (w *writeFile) Lock() error                       { return billy.ErrNotSupported }
func (w *writeFile) Unlock() error                     { return billy.ErrNotSupported }

// fileInfo is the os.FileInfo for an object or prefix.
type fileInfo struct {
	name  string
	size  int64
	mtime time.Time
	dir   bool
}

func (i fileInfo) Name() string { return i.name }
func (i fileInfo) Size() int64  { return i.size }
func (i fileInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (i fileInfo) ModTime() time.Time { return i.mtime }
func (i fileInfo) IsDir() bool        { return i.dir }
func (i fileInfo) Sys() any           { return nil }
