// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"log"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/gcsx"
	"github.com/pkg/errors"
	"google.golang.org/api/googleapi"
)

// cacheBackend provides storage operations for both GCS and local filesystem.
type cacheBackend interface {
	// exists checks if the cache entry exists and returns its modification time.
	// Returns zero time and nil error if not found.
	exists(ctx context.Context, path string) (mtime time.Time, err error)
	// writer returns a WriteCloser for writing data to the cache entry.
	// The caller must call Close to finalize the write. Cancelling ctx before
	// Close abandons the write and leaves any previous entry in place.
	writer(ctx context.Context, path string) (io.WriteCloser, error)
	// reader returns the cache entry's contents.
	reader(ctx context.Context, path string) (io.ReadCloser, error)
	// serve serves the cached content to the response writer.
	serve(rw http.ResponseWriter, req *http.Request, path string)
	// delete removes the cache entry.
	delete(ctx context.Context, path string) error
	// lease claims the entry's fill, failing with errLeaseHeld while another
	// claim is live. The returned context ends with errLeaseLost once the
	// claim may have passed to another instance, so the fill runs under it.
	lease(ctx context.Context, path string) (held context.Context, release func(), err error)
}

// A lease holder renews its claim every leaseRenew, and a claim not renewed
// for leaseTTL lapses, so a holder that dies mid-clone frees the entry.
var (
	leaseRenew = 30 * time.Second
	leaseTTL   = 2 * time.Minute
)

const leaseSuffix = ".lease"

var (
	errLeaseHeld = errors.New("another request holds the fill lease")
	errLeaseLost = errors.New("fill lease lost")
)

// renewing renews a lease every leaseRenew until the returned func ends it.
// Both outlive ctx so that a cancelled request still releases its lease. The
// returned context ends with errLeaseLost when renew reports the claim gone,
// or when the claim could lapse before the next renewal.
func renewing(ctx context.Context, renew, end func(context.Context) error) (context.Context, func()) {
	held, lose := context.WithCancelCause(ctx)
	base := context.WithoutCancel(ctx)
	rctx, cancel := context.WithCancel(base)
	every, ttl := leaseRenew, leaseTTL
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		renewed := time.Now()
		for {
			select {
			case <-rctx.Done():
				return
			case <-t.C:
				cctx, ccancel := context.WithTimeout(rctx, every)
				err := renew(cctx)
				ccancel()
				switch {
				case err == nil:
					renewed = time.Now()
				case errors.Is(err, errLeaseLost) || time.Since(renewed)+every >= ttl:
					log.Printf("Lost fill lease: %v", err)
					lose(errLeaseLost)
					return
				default:
					log.Printf("Failed to renew fill lease: %v", err)
				}
			}
		}
	}()
	return held, func() {
		lose(nil)
		cancel()
		<-done
		ectx, cancel := context.WithTimeout(base, 10*time.Second)
		defer cancel()
		if err := end(ectx); err != nil {
			log.Printf("Failed to release fill lease: %v", err)
		}
	}
}

func isPreconditionFailed(err error) bool {
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == http.StatusPreconditionFailed
}

// gcsBackend implements cacheBackend for GCS storage.
type gcsBackend struct {
	client *storage.Client
	bucket string
}

func (g *gcsBackend) exists(ctx context.Context, path string) (time.Time, error) {
	o := g.client.Bucket(g.bucket).Object(path)
	a, err := o.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return a.Updated, nil
}

func (g *gcsBackend) reader(ctx context.Context, path string) (io.ReadCloser, error) {
	return g.client.Bucket(g.bucket).Object(path).NewReader(ctx)
}

func (g *gcsBackend) writer(ctx context.Context, path string) (io.WriteCloser, error) {
	o := g.client.Bucket(g.bucket).Object(path)
	return o.NewWriter(ctx), nil
}

func (g *gcsBackend) serve(rw http.ResponseWriter, req *http.Request, path string) {
	o := g.client.Bucket(g.bucket).Object(path)
	a, err := o.Attrs(req.Context())
	if err != nil {
		log.Printf("Failed to get attrs for gs://%s/%s: %v", g.bucket, path, err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Header().Set(sizeHeader, strconv.FormatInt(a.Size, 10))
	http.Redirect(rw, req, gcsx.MediaURL(g.bucket, path, a.Generation), http.StatusFound)
}

func (g *gcsBackend) delete(ctx context.Context, path string) error {
	o := g.client.Bucket(g.bucket).Object(path)
	err := o.Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

// lease creates the lease object only if absent and renews it by updating its
// metadata, which advances its update time. Renewals and deletes match the
// generation, so a lease re-created meanwhile survives and its holder's
// predecessor learns it lost the claim.
func (g *gcsBackend) lease(ctx context.Context, path string) (context.Context, func(), error) {
	o := g.client.Bucket(g.bucket).Object(path + leaseSuffix)
	for range 2 {
		w := o.If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
		if err := w.Close(); err == nil {
			mine := o.If(storage.Conditions{GenerationMatch: w.Attrs().Generation})
			held, release := renewing(ctx, func(ctx context.Context) error {
				_, err := mine.Update(ctx, storage.ObjectAttrsToUpdate{Metadata: map[string]string{"renewed": time.Now().UTC().Format(time.RFC3339)}})
				if isPreconditionFailed(err) || errors.Is(err, storage.ErrObjectNotExist) {
					return errLeaseLost
				}
				return err
			}, mine.Delete)
			return held, release, nil
		} else if !isPreconditionFailed(err) {
			return nil, nil, errors.Wrap(err, "creating lease")
		}
		a, err := o.Attrs(ctx)
		if errors.Is(err, storage.ErrObjectNotExist) {
			continue // released since the create
		} else if err != nil {
			return nil, nil, errors.Wrap(err, "reading lease")
		} else if time.Since(a.Updated) < leaseTTL {
			return nil, nil, errLeaseHeld
		}
		err = o.If(storage.Conditions{GenerationMatch: a.Generation}).Delete(ctx)
		if err != nil && !isPreconditionFailed(err) && !errors.Is(err, storage.ErrObjectNotExist) {
			return nil, nil, errors.Wrap(err, "clearing lapsed lease")
		}
	}
	return nil, nil, errLeaseHeld
}

// localBackend implements cacheBackend for local filesystem storage.
type localBackend struct {
	baseDir string
}

func (l *localBackend) exists(ctx context.Context, path string) (time.Time, error) {
	fullPath := filepath.Join(l.baseDir, path)
	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// atomicFileWriter writes to a temp file and renames to the final path on
// Close, unless ctx ended first.
type atomicFileWriter struct {
	ctx       context.Context
	finalPath string
	tmpFile   *os.File
}

func (w *atomicFileWriter) Write(p []byte) (int, error) {
	return w.tmpFile.Write(p)
}

func (w *atomicFileWriter) Close() error {
	tmpPath := w.tmpFile.Name()
	if err := w.tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return errors.Wrap(err, "closing temp file")
	}
	if err := w.ctx.Err(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, w.finalPath); err != nil {
		os.Remove(tmpPath)
		return errors.Wrap(err, "renaming temp file to final path")
	}
	return nil
}

func (l *localBackend) reader(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(l.baseDir, path))
}

func (l *localBackend) writer(ctx context.Context, path string) (io.WriteCloser, error) {
	fullPath := filepath.Join(l.baseDir, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, errors.Wrap(err, "creating cache directory")
	}
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return nil, errors.Wrap(err, "creating temp file")
	}
	return &atomicFileWriter{ctx: ctx, finalPath: fullPath, tmpFile: tmpFile}, nil
}

func (l *localBackend) serve(rw http.ResponseWriter, req *http.Request, path string) {
	fullPath := filepath.Join(l.baseDir, path)
	http.ServeFile(rw, req, fullPath)
}

func (l *localBackend) delete(ctx context.Context, path string) error {
	fullPath := filepath.Join(l.baseDir, path)
	err := os.Remove(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// lease creates the file with O_EXCL and renews it by touching its mtime. The
// file holds a token, so a holder whose claim lapsed and was taken neither
// renews nor removes its successor's.
func (l *localBackend) lease(ctx context.Context, path string) (context.Context, func(), error) {
	p := filepath.Join(l.baseDir, path+leaseSuffix)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, nil, errors.Wrap(err, "creating cache directory")
	}
	token := []byte(rand.Text())
	mine := func() bool {
		b, err := os.ReadFile(p)
		return err == nil && bytes.Equal(b, token)
	}
	for range 2 {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = f.Write(token)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(p)
				return nil, nil, errors.Wrap(err, "writing lease")
			}
			held, release := renewing(ctx, func(context.Context) error {
				if !mine() {
					return errLeaseLost
				}
				now := time.Now()
				return os.Chtimes(p, now, now)
			}, func(context.Context) error {
				if !mine() {
					return nil
				}
				return os.Remove(p)
			})
			return held, release, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, nil, errors.Wrap(err, "creating lease")
		}
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // released since the create
		} else if err != nil {
			return nil, nil, errors.Wrap(err, "reading lease")
		} else if time.Since(info.ModTime()) < leaseTTL {
			return nil, nil, errLeaseHeld
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.Wrap(err, "clearing lapsed lease")
		}
	}
	return nil, nil, errLeaseHeld
}
