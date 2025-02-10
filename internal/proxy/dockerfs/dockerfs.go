// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package dockerfs defines a FS interface for accessing files in a Docker container.
package dockerfs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

const (
	statTimeFormat = "2006-01-02T15:04:05Z07:00"
	statHeader     = "X-Docker-Container-Path-Stat"
)

// Filesystem exposes files in a Docker container via a FS-compatible API.
// NOTE: Client accesses will not use Scheme or Host URI elements.
type Filesystem struct {
	Client    httpx.BasicClient
	Container string
}

// Open returns a File from a Docker container.
func (c Filesystem) Open(path string) (*File, error) {
	log.Printf("Open for path: %s", path)
	if !filepath.IsAbs(path) {
		return nil, fs.ErrInvalid
	}
	req, err := http.NewRequest(http.MethodGet, "/containers/"+c.Container+"/archive?path="+path, http.NoBody)
	if err != nil {
		return nil, errors.Wrap(err, "building request")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "making request")
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fs.ErrNotExist
	case http.StatusBadRequest:
		return nil, fs.ErrNotExist
	default:
		return nil, errors.Wrap(errors.New(resp.Status), "response error: Unexpected HTTP response")
	}
	tr := tar.NewReader(resp.Body)
	hdr, err := tr.Next()
	if err == io.EOF {
		return nil, errors.New("response error: No records found")
	}
	if err != nil {
		return nil, errors.Wrap(err, "reading tar header")
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return nil, errors.Wrap(err, "reading tar content")
	}
	if _, err := tr.Next(); err != io.EOF {
		return nil, fs.ErrInvalid // NOTE: dirs are unsupported.
	}
	return &File{path, *hdr, b}, nil
}

// Stat returns the FileInfo of a file from a Docker container.
func (c Filesystem) Stat(path string) (*FileInfo, error) {
	log.Printf("Stat for path: %s", path)
	if !filepath.IsAbs(path) {
		return nil, fs.ErrInvalid
	}
	req, err := http.NewRequest(http.MethodHead, "/containers/"+c.Container+"/archive?path="+path, http.NoBody)
	if err != nil {
		return nil, errors.Wrap(err, "building request")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "making request")
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fs.ErrNotExist
	case http.StatusBadRequest:
		return nil, errors.New("request error: bad parameter")
	default:
		return nil, errors.Wrap(errors.New(resp.Status), "response error: Unexpected HTTP response")
	}
	encoded := resp.Header.Get(statHeader)
	if encoded == "" {
		return nil, errors.New("empty stat header")
	}
	b64r := base64.NewDecoder(base64.URLEncoding, strings.NewReader(encoded))
	jr := json.NewDecoder(b64r)
	var ds dockerStat
	if err := jr.Decode(&ds); err != nil {
		return nil, errors.New("failed to decode stat header json")
	}
	t, err := time.Parse(statTimeFormat, ds.MTime)
	if err != nil {
		return nil, errors.New("invalid time in stat json")
	}
	return &FileInfo{name: ds.Name, size: ds.Size, mode: fs.FileMode(ds.Mode), modTime: t, LinkTarget: ds.LinkTarget}, nil
}

// OpenAndResolve returns the file from the given container, resolving any symlinks encountered.
func (c Filesystem) OpenAndResolve(path string) (*File, error) {
	log.Printf("OpenAndResolve for path: %s", path)
	// NOTE: 255 is the repetition threshold used by filepath.EvalSymlinks.
	for range 255 {
		fi, err := c.Stat(path)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return c.Open(path)
		}
		linkPath := fi.LinkTarget
		if !filepath.IsAbs(linkPath) {
			// FIXME: This path demangling doesn't work if invoked from Windows.
			linkPath = filepath.Join(filepath.Dir(path), linkPath)
		}
		path = linkPath
	}
	return nil, errors.New("too many links")
}

type dockerStat struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Mode       int64  `json:"mode"`
	MTime      string `json:"mtime"`
	LinkTarget string `json:"linkTarget"`
}

// FileInfo implements fs.FileInfo.
type FileInfo struct {
	name       string
	size       int64
	mode       fs.FileMode
	modTime    time.Time
	LinkTarget string
}

// NewFileInfo builds a new FileInfo.
func NewFileInfo(name string, size int64, mode fs.FileMode, modTime time.Time, linkTarget string) FileInfo {
	return FileInfo{name: name, size: size, mode: mode, modTime: modTime, LinkTarget: linkTarget}
}

// Name returns the file name.
func (fi FileInfo) Name() string { return fi.name }

// Size returns the file size in bytes.
func (fi FileInfo) Size() int64 { return fi.size }

// Mode returns the FileMode.
func (fi FileInfo) Mode() fs.FileMode { return fi.mode }

// ModTime returns the last modification time.
func (fi FileInfo) ModTime() time.Time { return fi.modTime }

// IsDir returns whether this corresponds to a directory.
func (fi FileInfo) IsDir() bool { return fi.Mode().IsDir() }

// Sys returns the underlying filesystem metadata for the FileInfo.
func (fi FileInfo) Sys() any { return fi }

// Resolve attempts to resolve the symlink of the provided file.
func (c Filesystem) Resolve(f *File) (*File, error) {
	log.Printf("Resolve for file: %s", f.Path)
	s, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if s.Mode().Type()&fs.ModeSymlink == 0 {
		return f, nil
	}
	linkPath := f.Metadata.Linkname
	if !filepath.IsAbs(linkPath) {
		linkPath = filepath.Join(filepath.Dir(f.Path), f.Metadata.Linkname)
	}
	newF, err := c.Open(linkPath)
	if err != nil {
		return nil, err
	}
	return newF, err
}

// WriteFile writes the contents of the file back to the Docker container.
func (c Filesystem) WriteFile(f *File) error {
	log.Printf("WriteFile for file: %s", f.Path)
	req, err := http.NewRequest(http.MethodPut, "/containers/"+c.Container+"/archive?path="+filepath.Dir(f.Path), nil)
	if err != nil {
		return errors.Wrap(err, "request building error")
	}
	req.Header.Set("Content-Type", "application/x-tar")
	var archive bytes.Buffer
	w := bufio.NewWriter(&archive)
	tw := tar.NewWriter(w)
	f.Metadata.Size = int64(len(f.Contents))
	if err := tw.WriteHeader(&f.Metadata); err != nil {
		return errors.Wrap(err, "writing tar header")
	}
	if _, err := tw.Write(f.Contents); err != nil {
		return errors.Wrap(err, "writing tar content")
	}
	if err := tw.Close(); err != nil {
		return errors.Wrap(err, "flushing tar")
	}
	w.Flush()
	req.Body = io.NopCloser(bytes.NewReader(archive.Bytes()))
	resp, err := c.Client.Do(req)
	if err != nil {
		return errors.Wrap(err, "making request")
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fs.ErrNotExist
	case http.StatusBadRequest:
		// TODO: Confirm the conditions under which this occurs.
		return fs.ErrNotExist
	default:
		return errors.Wrap(errors.New(resp.Status), "unexpected HTTP response")
	}
}

// File represents a file in a Docker container.
type File struct {
	Path     string
	Metadata tar.Header
	Contents []byte
}

// Stat returns the file's FileInfo.
func (c File) Stat() (fs.FileInfo, error) { return c.Metadata.FileInfo(), nil }

// Read records the file's contents to the provided buffer.
func (c File) Read(buf []byte) (int, error) { return copy(buf, c.Contents), nil }

// Close is a no-op.
func (File) Close() error { return nil }
