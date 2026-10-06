// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dockerfs

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func makeStat(t *testing.T, fi FileInfo) string {
	t.Helper()
	ds := dockerStat{
		Name:       fi.Name(),
		Mode:       int64(fi.Mode()),
		Size:       fi.Size(),
		MTime:      fi.ModTime().Format(statTimeFormat),
		LinkTarget: fi.LinkTarget,
	}
	b := must(json.Marshal(ds))
	buf := new(bytes.Buffer)
	b64e := base64.NewEncoder(base64.StdEncoding, buf)
	must(b64e.Write(b))
	b64e.Close()
	return buf.String()
}
func withHeader(header, value string) http.Header {
	h := make(http.Header)
	h.Add(header, value)
	return h
}

func makeOpen(t *testing.T, fi FileInfo, content, linkTarget string) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	w := tar.NewWriter(b)
	h := must(tar.FileInfoHeader(fi, linkTarget))
	must1(w.WriteHeader(h))
	must(w.Write([]byte(content)))
	w.Close()
	return b.Bytes()
}

var someTime = time.Unix(1234567890, 0).UTC()

func TestOpen(t *testing.T) {
	wantContents := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n"
	wantStat := FileInfo{name: "release", mode: fs.ModePerm, size: int64(len(wantContents)), modTime: someTime}
	osTarBytes := makeOpen(t, wantStat, wantContents, "")
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.Open("/etc/release"))
	if string(got.Contents) != wantContents {
		t.Fatalf("Unexpected Open contents: want=%s got=%s", wantContents, string(got.Contents))
	}
	fi := must(got.Stat())
	if fi.Name() != wantStat.Name() {
		t.Fatalf("Unexpected Open FileInfo name: want=%s got=%s", wantStat.Name(), fi.Name())
	}
	if fi.Mode() != wantStat.Mode() {
		t.Fatalf("Unexpected Open FileInfo mode: want=%v got=%v", wantStat.Mode(), fi.Mode())
	}
	if fi.Size() != wantStat.Size() {
		t.Fatalf("Unexpected Open FileInfo size: want=%d got=%d", wantStat.Size(), fi.Size())
	}
}

func TestStat(t *testing.T) {
	want := FileInfo{name: "release", mode: fs.ModePerm, size: 12, modTime: someTime}
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "HEAD", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, want))}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.Stat("/etc/release"))
	if *got != want {
		t.Fatalf("Unexpected Stat result: want=%v got=%v", want, *got)
	}
}

func TestStatHeaderNonURLSafe(t *testing.T) {
	// NOTE: The daemon encodes the stat header with std base64 whose '+' and
	// '/' characters are rejected by URL-safe decoders. This name is chosen
	// such that its encoding includes one.
	want := FileInfo{name: "cache~", mode: fs.ModePerm, size: 12, modTime: someTime}
	encoded := makeStat(t, want)
	if !strings.ContainsAny(encoded, "+/") {
		t.Fatalf("Test payload must exercise std-only base64 chars: %s", encoded)
	}
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "HEAD", URL: "/containers/abc/archive?path=/tmp/cache~", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, encoded)}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.Stat("/tmp/cache~"))
	if *got != want {
		t.Fatalf("Unexpected Stat result: want=%v got=%v", want, *got)
	}
}

func TestOpenAndResolve(t *testing.T) {
	symStat := FileInfo{name: "release", mode: fs.ModePerm | fs.ModeSymlink, size: 25, modTime: someTime, LinkTarget: "../os-release"}
	wantContents := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n"
	wantStat := FileInfo{name: "os-release", mode: fs.ModePerm, size: int64(len(wantContents)), modTime: someTime}
	osTarBytes := makeOpen(t, wantStat, wantContents, "")
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "HEAD", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, symStat))}},
			{Method: "HEAD", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, wantStat))}},
			{Method: "GET", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.OpenAndResolve("/etc/release"))
	if string(got.Contents) != wantContents {
		t.Fatalf("Unexpected OpenAndResolve contents: want=%s got=%s", wantContents, string(got.Contents))
	}
	fi := must(got.Stat())
	if fi.Name() != wantStat.Name() {
		t.Fatalf("Unexpected OpenAndResolve FileInfo name: want=%s got=%s", wantStat.Name(), fi.Name())
	}
	if fi.Mode() != wantStat.Mode() {
		t.Fatalf("Unexpected OpenAndResolve FileInfo mode: want=%v got=%v", wantStat.Mode(), fi.Mode())
	}
	if fi.Size() != wantStat.Size() {
		t.Fatalf("Unexpected OpenAndResolve FileInfo size: want=%d got=%d", wantStat.Size(), fi.Size())
	}
}

func TestResolve(t *testing.T) {
	symStat := FileInfo{name: "release", mode: fs.ModePerm | fs.ModeSymlink, size: 25, modTime: someTime, LinkTarget: "../os-release"}
	symTarBytes := makeOpen(t, symStat, "", "../os-release")
	wantContents := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n"
	wantStat := FileInfo{name: "os-release", mode: fs.ModePerm, size: int64(len(wantContents)), modTime: someTime}
	osTarBytes := makeOpen(t, wantStat, wantContents, "")
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(symTarBytes))}},
			{Method: "GET", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.Open("/etc/release"))
	if got.Metadata.FileInfo().Mode() != symStat.Mode() {
		t.Fatalf("Unexpected Open symlink Mode: want=%v got=%v", symStat.Mode(), got.Metadata.FileInfo().Mode())
	}
	got = must(f.Resolve(got))
	if string(got.Contents) != wantContents {
		t.Fatalf("Unexpected Resolve contents: want=%s got=%s", wantContents, string(got.Contents))
	}
	fi := must(got.Stat())
	if fi.Name() != wantStat.Name() {
		t.Fatalf("Unexpected Resolve FileInfo name: want=%s got=%s", wantStat.Name(), fi.Name())
	}
	if fi.Mode() != wantStat.Mode() {
		t.Fatalf("Unexpected Resolve FileInfo mode: want=%v got=%v", wantStat.Mode(), fi.Mode())
	}
	if fi.Size() != wantStat.Size() {
		t.Fatalf("Unexpected Resolve FileInfo size: want=%d got=%d", wantStat.Size(), fi.Size())
	}
}

func TestWriteFile(t *testing.T) {
	wantContents := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n"
	wantStat := FileInfo{name: "release", mode: fs.ModePerm, size: int64(len(wantContents)), modTime: someTime}
	osTarBytes := makeOpen(t, wantStat, wantContents, "")
	f := Filesystem{Client: &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
			{Method: "PUT", URL: "/containers/abc/archive?path=/etc", Response: &http.Response{StatusCode: http.StatusOK}},
		},
		URLValidator: httpxtest.NewURLValidator(t),
	}, Container: "abc"}
	got := must(f.Open("/etc/release"))
	if string(got.Contents) != wantContents {
		t.Fatalf("Unexpected Open contents: want=%s got=%s", wantContents, string(got.Contents))
	}
	fi := must(got.Stat())
	if fi.Name() != wantStat.Name() {
		t.Fatalf("Unexpected Open FileInfo name: want=%s got=%s", wantStat.Name(), fi.Name())
	}
	if fi.Mode() != wantStat.Mode() {
		t.Fatalf("Unexpected Open FileInfo mode: want=%v got=%v", wantStat.Mode(), fi.Mode())
	}
	if fi.Size() != wantStat.Size() {
		t.Fatalf("Unexpected Open FileInfo size: want=%d got=%d", wantStat.Size(), fi.Size())
	}
	got.Contents = []byte(wantContents + "EXTRA=PROPERTY\n")
	must1(f.WriteFile(got))
}

func must1(err error) {
	if err != nil {
		panic(err)
	}
}

func must[T any](t T, err error) T {
	must1(err)
	return t
}

func TestFileRead(t *testing.T) {
	type readResult struct {
		N    int
		Data string
		Err  error
	}
	for _, tc := range []struct {
		name     string
		contents string
		sizes    []int
		want     []readResult
	}{
		{"Chunks", "abcdef", []int{3, 3, 3}, []readResult{{3, "abc", nil}, {3, "def", nil}, {0, "", io.EOF}}},
		{"Uneven", "abcdef", []int{4, 4, 1}, []readResult{{4, "abcd", nil}, {2, "ef", nil}, {0, "", io.EOF}}},
		{"Empty", "", []int{3}, []readResult{{0, "", io.EOF}}},
		{"EmptyBuffer", "abcdef", []int{0, 2, 0, 8, 0, 1}, []readResult{{0, "", nil}, {2, "ab", nil}, {0, "", nil}, {4, "cdef", nil}, {0, "", nil}, {0, "", io.EOF}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &File{Contents: []byte(tc.contents)}
			for i, size := range tc.sizes {
				buf := make([]byte, size)
				n, err := f.Read(buf)
				got := readResult{n, string(buf[:n]), err}
				if diff := cmp.Diff(tc.want[i], got, cmp.Comparer(func(a, b error) bool { return a == b })); diff != "" {
					t.Fatalf("Read %d mismatch (-want +got):\n%s", i, diff)
				}
			}
			if diff := cmp.Diff(tc.contents, string(f.Contents)); diff != "" {
				t.Errorf("Contents changed (-want +got):\n%s", diff)
			}
		})
	}
}
