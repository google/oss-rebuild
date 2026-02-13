// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"archive/zip"
	"log"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	apb "google.golang.org/protobuf/types/known/anypb"
)

type fileStat struct {
	name string
	size int64
}

// BufferedReadCloser has all of the methods
// from *bufio.Reader and io.ReadWriteCloser.
type BufferedReadCloser struct {
	*bufio.Reader
	io.Closer
}

func (rw *BufferedReadCloser) Read(p []byte) (int, error) {
	return rw.Reader.Read(p)
}

// BufferedWriteCloser has all of the methods
// from *bufio.Reader and io.ReadWriteCloser.
type BufferedWriteCloser struct {
	*bufio.Writer
	io.Closer
}

func (rw *BufferedWriteCloser) Write(p []byte) (int, error) {
	return rw.Writer.Write(p)
}

// Close implements io.Closer.
// It flushes the buffer and closes the underlying file.
func (rw *BufferedWriteCloser) Close() error {
	if err := rw.Writer.Flush(); err != nil {
		return err
	}
	return rw.Closer.Close()
}

// RawActionFileName returns the file name for the raw events of the action with the given id.
func RawActionFileName(id int64) string {
	return ActionFileName(id) + RawEventsFileNameSuffix
}

// ActionFileName returns the file name for the action with the given id.
func ActionFileName(id int64) string {
	return strconv.FormatInt(id, 10)
}

const (
	// ActionDirName is the canonical name of action directory.
	ActionDirName = "a"
	// GraphProtoFileName is the canonical name of the sysgraph metadata proto file.
	GraphProtoFileName = "graph"
	// RDBProtoFileName is the canonical name of the resource database proto file.
	RDBProtoFileName = "rdb"
	// RawEventsFileNameSuffix is the suffix of the file name for raw events.
	RawEventsFileNameSuffix = "_raw_events"
)

// writerFS is an interface for file system operations.
// TODO: use go-billy or another vfs.
type writerFS interface {
	MkdirAll(ctx context.Context, path string, perm os.FileMode) error
	WriteFile(ctx context.Context, path string, blob []byte) error
	FileWriter(ctx context.Context, path string) (io.WriteCloser, error)
}

// diskFS is a FileFS implementation that writes to disk.
type diskFS string

func (d diskFS) String() string {
	return string(d)
}

var _ writerFS = (*diskFS)(nil)

// MkdirAll creates all directories in the path.
func (d diskFS) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	return os.MkdirAll(filepath.Join(string(d), path), perm)
}

// WriteFile writes a file to disk.
func (d diskFS) WriteFile(ctx context.Context, path string, blob []byte) error {
	return errors.WithMessage(os.WriteFile(filepath.Join(string(d), path), blob, 0644), "while writing file")
}

// FileWriter returns a buffered writer for a file in disk.
func (d diskFS) FileWriter(ctx context.Context, path string) (io.WriteCloser, error) {
	f, err := os.OpenFile(filepath.Join(string(d), path), os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("while opening file: %w", err)
	}
	return &BufferedWriteCloser{bufio.NewWriter(f), f}, nil
}

// zipFs is a FileFS implementation that writes to a zip file.
type zipFs struct {
	*zip.Writer
	zipPath string
	zipFile io.Closer
	mu      sync.Mutex
}

func (fs *zipFs) String() string {
	return fmt.Sprintf("ZipFileFS{%s}", fs.zipPath)
}

var _ writerFS = (*zipFs)(nil)

func (fs *zipFs) Close() error {
	if err := fs.Writer.Close(); err != nil {
		return fmt.Errorf("while closing zip file: %w", err)
	}
	return fs.zipFile.Close()
}

// MkdirAll is a no-op for ZipFileFS.
// Zip files do not support directories.
func (fs *zipFs) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	return nil
}

// WriteFile writes a file to zip.
func (fs *zipFs) WriteFile(ctx context.Context, path string, blob []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, err := fs.Create(path)
	if err != nil {
		return err
	}
	_, err = f.Write(blob)
	return err
}

// WriteCloser is a write closer that writes to memory.
type inmemoryWriteCloser struct {
	bytes.Buffer
	CloseFn func([]byte) error
}

// Close implements io.Closer.
func (w *inmemoryWriteCloser) Close() error {
	return w.CloseFn(w.Buffer.Bytes())
}

// FileWriter returns a buffered writer for a file in zip.
// All contents are buffered in memory until the writer is closed and the file is written to zip.
// This is done because only one file can be written to zip at a time.
func (fs *zipFs) FileWriter(ctx context.Context, path string) (io.WriteCloser, error) {
	return &inmemoryWriteCloser{CloseFn: func(blob []byte) error {
		return fs.WriteFile(ctx, path, blob)
	}}, nil
}

// Option is an option for the graph writer.
type Option interface{ set(*GraphWriter) } // a base type for the options
type option func(*GraphWriter)             // option implements Option.
func (o option) set(rpc *GraphWriter)      { o(rpc) }

// ProtoJSON returns an option to write proto messages in JSON format.
func ProtoJSON(v bool) Option {
	return option(func(w *GraphWriter) { w.protoJSON = v })
}

// Textproto returns an option to write proto messages in textproto format.
func Textproto(v bool) Option {
	return option(func(w *GraphWriter) { w.textproto = v })
}

// CopyPath returns an option to copy a directory of files
// (like tetragon logs) into the zip file.
func CopyPath(src, dst string) Option {
	return option(func(w *GraphWriter) {
		w.copyOps = append(w.copyOps, copyOp{src: src, dst: dst})
	})
}

// ProgressFunc returns an option to set the progress function to be called
// when writing a file.
func ProgressFunc(f func()) Option {
	return option(func(w *GraphWriter) {
		w.ProgressFunc = f
	})
}

type copyOp struct {
	src string
	dst string
}

// GraphWriter is a graph writer that writes to a file system.
type GraphWriter struct {
	fs           writerFS
	mkdirsOnce   sync.Once
	protoJSON    bool
	textproto    bool
	copyOps      []copyOp
	ProgressFunc func()
}

func (w *GraphWriter) String() string {
	return fmt.Sprintf("{FS: %v, ProtoJSON: %t, Textproto: %t}", w.fs, w.protoJSON, w.textproto)
}

// Close closes the underlying file system if it is a closer.
func (w *GraphWriter) Close() error {
	if closer, ok := w.fs.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// NewGraphWriter creates a new graph writer.
// This function should not be used directly. Use sgstorage.Write or sgir.Builder.SysGraph instead.
func NewGraphWriter(ctx context.Context, path string, opts ...Option) (*GraphWriter, error) {
	var fs writerFS
	var err error
	if strings.HasPrefix(path, "gs:") {
		if strings.HasSuffix(path, ".zip") {
			fs, err = gcsZipFileFS(ctx, path)
		} else {
			fs, err = newGCSFS(path)
		}
	} else if strings.HasSuffix(path, ".zip") {
		fs, err = zipFileFS(ctx, path)
	} else {
		fs = diskFS(path)
	}
	if err != nil {
		return nil, err
	}
	writer := &GraphWriter{fs: fs}
	for _, opt := range opts {
		opt.set(writer)
	}
	if writer.ProgressFunc == nil {
		writer.ProgressFunc = func() {}
	}
	return writer, nil
}

func zipFileFS(ctx context.Context, path string) (*zipFs, error) {
	zipFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	return &zipFs{Writer: zip.NewWriter(zipFile), zipPath: path, zipFile: zipFile}, nil
}

func gcsZipFileFS(ctx context.Context, path string) (*zipFs, error) {
	fs, err := newGCSFS(path)
	if err != nil {
		return nil, err
	}
	_, object, err := parseGCSPath(path)
	if err != nil {
		return nil, err
	}
	zipFile, err := fs.FileWriter(ctx, object)
	if err != nil {
		return nil, err
	}
	return &zipFs{Writer: zip.NewWriter(zipFile), zipPath: path, zipFile: zipFile}, nil
}

func (w *GraphWriter) mkdirs(ctx context.Context) error {
	w.mkdirsOnce.Do(func() {
		if err := w.fs.MkdirAll(ctx, ActionDirName, 0755); err != nil {
			log.Printf("Failed to create directories: %v", err)
		}
	})
	return nil
}

// WriteAction writes the action to disk.
func (w *GraphWriter) WriteAction(ctx context.Context, action *sgpb.Action) error {
	return w.writeMessage(ctx, ActionDirName, ActionFileName(action.GetId()), action)
}

// WriteRDB writes the resource database to disk.
func (w *GraphWriter) WriteRDB(ctx context.Context, rdb *sgpb.ResourceDB) error {
	return w.writeMessage(ctx, "", RDBProtoFileName, rdb)
}

// WriteGraphProto writes the sysgraph proto to disk.
func (w *GraphWriter) WriteGraphProto(ctx context.Context, graph *sgpb.SysGraph) error {
	return w.writeMessage(ctx, "", GraphProtoFileName, graph)
}

// WriteRawEvents writes the raw events to disk.
func (w *GraphWriter) WriteRawEvents(ctx context.Context, id int64, events <-chan *apb.Any) error {
	w.mkdirs(ctx)
	chProto, chJSON := make(chan *apb.Any), make(chan *apb.Any)
	go func() {
		defer close(chProto)
		defer close(chJSON)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				w.ProgressFunc()
				if !ok {
					return
				}
				chJSON <- event
				chProto <- event
			}
		}
	}()
	eg, eCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return w.WriteRawEventsJSON(eCtx, ActionDirName, ActionFileName(id), chJSON)
	})
	eg.Go(func() error {
		return w.WriteRawEventsProto(eCtx, ActionDirName, ActionFileName(id), chProto)
	})
	return eg.Wait()
}

// WriteRawEventsJSON writes the raw events to disk.
func (w *GraphWriter) WriteRawEventsJSON(ctx context.Context, dir, fname string, events <-chan *apb.Any) error {
	fpath := filepath.Join(dir, fname+RawEventsFileNameSuffix)
	log.Printf("writing %s", fpath)
	file, err := w.fs.FileWriter(ctx, fpath+".jsonl")
	if err != nil {
		return err
	}
	defer file.Close()
	for event := range events {
		eventBytes, err := protojson.Marshal(event)
		if err != nil {
			log.Printf("Failed to marshal event: %v", err)
			continue
		}
		eventBytes = append(eventBytes, '\n')
		if _, err := file.Write(eventBytes); err != nil {
			return err
		}
	}
	return nil
}

// WriteRawEventsProto writes the raw events to disk.
func (w *GraphWriter) WriteRawEventsProto(ctx context.Context, dir, fname string, events <-chan *apb.Any) error {
	fpath := filepath.Join(dir, fname+RawEventsFileNameSuffix)
	log.Printf("writing %s (%d events)", fpath, len(events))
	file, err := w.fs.FileWriter(ctx, fpath+".pbdelim")
	if err != nil {
		return err
	}
	defer file.Close()
	for event := range events {
		if _, err := protodelim.MarshalTo(file, event); err != nil {
			return err
		}
	}
	return nil
}

func (w *GraphWriter) writeMessage(ctx context.Context, dir, fname string, m proto.Message) error {
	w.ProgressFunc()
	defer w.ProgressFunc()
	w.mkdirs(ctx)
	blob, err := proto.Marshal(m)
	if err != nil {
		return err
	}

	if err := w.fs.WriteFile(ctx, filepath.Join(dir, fname+".pb"), blob); err != nil {
		return err
	}
	if w.textproto {
		blob, err = prototext.MarshalOptions{Multiline: true}.Marshal(m)
		if err != nil {
			return err
		}
		if err := w.fs.WriteFile(ctx, filepath.Join(dir, fname+".txtpb"), blob); err != nil {
			return err
		}
	}
	if w.protoJSON {
		blob, err = protojson.MarshalOptions{Multiline: true}.Marshal(m)
		if err != nil {
			return err
		}
		if err := w.fs.WriteFile(ctx, filepath.Join(dir, fname+".json"), blob); err != nil {
			return err
		}
	}
	return nil
}

func fileWriter(ctx context.Context, fpath string, append bool) (*BufferedWriteCloser, error) {
	if strings.HasPrefix(fpath, "gs:") {
		return gcsFileWriter(ctx, fpath)
	}
	perms := os.O_WRONLY | os.O_CREATE
	if append {
		perms |= os.O_APPEND
	}
	f, err := os.OpenFile(fpath, perms, 0644)
	if err != nil {
		return nil, err
	}
	return &BufferedWriteCloser{bufio.NewWriter(f), f}, nil
}
