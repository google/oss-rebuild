package dockerfs

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
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
	b64e := base64.NewEncoder(base64.URLEncoding, buf)
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
	c := httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: func(expected, actual string) {
			if diff := cmp.Diff(expected, actual); diff != "" {
				t.Fatalf("URL mismatch (-want +got):\n%s", diff)
			}
		},
	}
	f := Filesystem{Client: &c, Container: "abc"}
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
	c := httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "HEAD", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, want))}},
		},
		URLValidator: func(expected, actual string) {
			if diff := cmp.Diff(expected, actual); diff != "" {
				t.Fatalf("URL mismatch (-want +got):\n%s", diff)
			}
		},
	}
	f := Filesystem{Client: &c, Container: "abc"}
	got := must(f.Stat("/etc/release"))
	if *got != want {
		t.Fatalf("Unexpected Stat result: want=%v got=%v", want, *got)
	}
}

func TestOpenAndResolve(t *testing.T) {
	symStat := FileInfo{name: "release", mode: fs.ModePerm | fs.ModeSymlink, size: 25, modTime: someTime, LinkTarget: "../os-release"}
	wantContents := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n"
	wantStat := FileInfo{name: "os-release", mode: fs.ModePerm, size: int64(len(wantContents)), modTime: someTime}
	osTarBytes := makeOpen(t, wantStat, wantContents, "")
	c := httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "HEAD", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, symStat))}},
			{Method: "HEAD", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Header: withHeader(statHeader, makeStat(t, wantStat))}},
			{Method: "GET", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: func(expected, actual string) {
			if diff := cmp.Diff(expected, actual); diff != "" {
				t.Fatalf("URL mismatch (-want +got):\n%s", diff)
			}
		},
	}
	f := Filesystem{Client: &c, Container: "abc"}
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
	c := httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(symTarBytes))}},
			{Method: "GET", URL: "/containers/abc/archive?path=/os-release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
		},
		URLValidator: func(expected, actual string) {
			if diff := cmp.Diff(expected, actual); diff != "" {
				t.Fatalf("URL mismatch (-want +got):\n%s", diff)
			}
		},
	}
	f := Filesystem{Client: &c, Container: "abc"}
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
	c := httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Method: "GET", URL: "/containers/abc/archive?path=/etc/release", Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(osTarBytes))}},
			{Method: "PUT", URL: "/containers/abc/archive?path=/etc", Response: &http.Response{StatusCode: http.StatusOK}},
		},
		URLValidator: func(expected, actual string) {
			if diff := cmp.Diff(expected, actual); diff != "" {
				t.Fatalf("URL mismatch (-want +got):\n%s", diff)
			}
		},
	}
	f := Filesystem{Client: &c, Container: "abc"}
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
