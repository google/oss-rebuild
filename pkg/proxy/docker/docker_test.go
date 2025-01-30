package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var PEM = []byte(`-----BEGIN CERTIFICATE-----
MIIDczCCAlugAwIBAgICCVowDQYJKoZIhvcNAQELBQAwUDELMAkGA1UEBhMCVVMx
FjAUBgNVBAcTDU5ldyBZb3JrIENpdHkxEzARBgNVBAoTCkdvb2dsZSBMTEMxFDAS
BgNVBAMTC0J1aWxkIFByb3h5MB4XDTIyMDYyOTAyMjk0MFoXDTIyMDcwNjAyMjk0
MFowUDELMAkGA1UEBhMCVVMxFjAUBgNVBAcTDU5ldyBZb3JrIENpdHkxEzARBgNV
BAoTCkdvb2dsZSBMTEMxFDASBgNVBAMTC0J1aWxkIFByb3h5MIIBIjANBgkqhkiG
9w0BAQEFAAOCAQ8AMIIBCgKCAQEArNb/kgcGzysQ2yLj3uTmNjsNn5kR9Vib3In3
I4oc7+MEa9vUe7gYMX0eIeBa+M8ZYpmCs3dct9iV5gdmLvEAjUiDV6RrlcQANJld
yBHovspqbaYgiWBS8tismDFukWXBShXjjIibV4tOhSN1LlstEQ/n+gHuv2brH5Se
yJC/4Dl61n4xHhkrmkO83JAYlPdDRRZsDjHzCH4Nq8FRg3eWZvDwKkJbeI7+VWlk
5hfP64YDItR0uGL7ImebyoQbJxAJVUA/PHMVisDq3mmOMXiVMRx+m1fJaEANRHRw
/v2Cl0Z4JaIpS2FKrCrutLjSFamIjI3xKCDFW4B84nxVNGpjOwIDAQABo1cwVTAO
BgNVHQ8BAf8EBAMCAoQwEwYDVR0lBAwwCgYIKwYBBQUHAwEwDwYDVR0TAQH/BAUw
AwEB/zAdBgNVHQ4EFgQUtm56/qtkbtCh6K84DNVQ3Tkig6EwDQYJKoZIhvcNAQEL
BQADggEBAD+wi1K0TVbgzrSalztcN2ohEkHIB7ywTxXXemnq1s/NZrEi4eyrNVK0
CVejfnhvQBpinDJQD1hYegZtyDt7SnSk5I3m471Z4nSKkjSHcS23v2dQ4yccZ0tx
BAGw9W8A8hGNcpHTjkVK0K5YetDFT3n9WTYnl2U6ALPcoXrG9mZeIagFF06xQuqX
lFM0lw73bbF/W3NYzwe7j7uZqHNwsT8P6TbnhnIsntiRMQptTDX2wyuKBuAV2LSj
bNa8KpdZ2qSaEDCvEWupUk+EtFGar/s+QnySXe8VQXbmNx40Plyucpf8MFkrQShu
pnFfADG87W05vBQr6O/cfLjtpkj1RBQ=
-----END CERTIFICATE-----
`)
var CERT = toCert(PEM)

func toCert(pemCert []byte) x509.Certificate {
	b, _ := pem.Decode(pemCert)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		panic(err)
	}
	return *c
}

func tempSocketName(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test*.sock")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func asBody(body []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(body))
}

type httpCall struct {
	RequestURI string
	Response   http.Response
}

func fakeHTTPCalls(l net.Listener, calls []httpCall, done chan<- error) {
	m := new(sync.Mutex)
	var i int
	err := http.Serve(l, http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		m.Lock()
		defer m.Unlock()
		defer func() { i++ }()
		if i >= len(calls) {
			done <- fmt.Errorf("Unexpected request: got=%s", req.RequestURI)
			return
		}
		call := calls[i]
		if req.RequestURI != call.RequestURI {
			done <- fmt.Errorf("Unexpected request URI: want=%s got=%s", call.RequestURI, req.RequestURI)
			return
		}
		io.ReadAll(req.Body)
		call.Response.Request = req
		for k, vs := range call.Response.Header {
			for _, v := range vs {
				rw.Header().Add(k, v)
			}
		}
		rw.WriteHeader(call.Response.StatusCode)
		if call.Response.Body == nil {
			call.Response.Body = http.NoBody
		}
		b, _ := io.ReadAll(call.Response.Body)
		if _, err := rw.Write(b); err != nil {
			done <- err
			return
		}
		done <- nil
	}))
	done <- err
}

func orFail(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setupProxy(t *testing.T, sockName string, dtp *ContainerTruststorePatcher) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sockName)
	orFail(t, err)
	clientIn, clientOut := net.Pipe()
	go dtp.proxyRequest(clientOut, c)
	return clientIn
}

func fileInfo(t *testing.T, name, content, linkTarget string) fs.FileInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if len(linkTarget) > 0 {
		orFail(t, os.Symlink(linkTarget, path))
	} else {
		orFail(t, os.WriteFile(path, []byte(content), fs.ModePerm))
	}
	fi, err := os.Stat(path)
	orFail(t, err)
	return fi
}

func asTar(t *testing.T, name, content, linkTarget string) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	w := tar.NewWriter(b)
	h, err := tar.FileInfoHeader(fileInfo(t, name, content, linkTarget), linkTarget)
	orFail(t, err)
	orFail(t, w.WriteHeader(h))
	_, err = w.Write([]byte(content))
	orFail(t, err)
	w.Close()
	return b.Bytes()
}

func asStatHeader(t *testing.T, name, content, linkTarget string) http.Header {
	t.Helper()
	h := make(http.Header)
	fi := fileInfo(t, name, content, linkTarget)
	j := fmt.Sprintf(`{"name":"%s","size":%d,"mode":420,"mtime":"2022-05-23T12:50:05-04:00","linkTarget":"%s"}`, fi.Name(), fi.Size(), linkTarget)
	buf := new(bytes.Buffer)
	b64e := base64.NewEncoder(base64.URLEncoding, buf)
	_, err := b64e.Write([]byte(j))
	orFail(t, err)
	b64e.Close()
	h.Add("X-Docker-Container-Path-Stat", buf.String())
	return h
}

func statAndOpen(t *testing.T, name, content, linkTarget string) (http.Header, io.ReadCloser) {
	return asStatHeader(t, name, content, linkTarget), asBody(asTar(t, name, content, linkTarget))
}

func TestProxyNormalReq(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	wants := []httpCall{
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
}

func TestPatchOnStart(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n", "")
	certHeader, certTar := statAndOpen(t, "cert.pem", "", "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Header: certHeader}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: certTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // POST /containers/abc/start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
}

func TestTryPatchOnStartUnknownOS(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Foo Linux\"\nID=foo\nVERSION_ID=0.0.0\n", "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // POST /containers/abc/start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
}

func TestUnpatchDuringExport(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n", "")
	certHeader, certTar := statAndOpen(t, "cert.pem", "", "")
	_, newCertTar := statAndOpen(t, "cert.pem", string(PEM), "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Header: certHeader}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: certTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Export container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: newCertTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/export", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // POST /start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Export container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodGet, "/containers/abc/export", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // GET /containers/abc/export
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	io.ReadAll(resp.Body)
	orFail(t, resp.Body.Close())
	// NOTE: Re-patch happens after export request is complete.
	orFail(t, <-outcome) // PUT /archive?path=/etc/ssl
}

func TestUnpatchDuringCommit(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n", "")
	certHeader, certTar := statAndOpen(t, "cert.pem", "", "")
	_, newCertTar := statAndOpen(t, "cert.pem", string(PEM), "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Header: certHeader}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: certTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Commit container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: newCertTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/commit?container=abc", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "sha256:foo"}`))}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // POST /start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Commit container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/commit?container=abc", asBody([]byte(`null`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // GET /containers/abc/export
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	io.ReadAll(resp.Body)
	orFail(t, resp.Body.Close())
	// NOTE: Re-patch happens after export request is complete.
	orFail(t, <-outcome) // PUT /archive?path=/etc/ssl
}

func TestPatchOnStartWithJavaEnv(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{JavaTruststoreEnvVar: true})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n", "")
	certHeader, certTar := statAndOpen(t, "cert.pem", "", "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/var/cache/proxy.crt.jks", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Header: certHeader}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: certTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt.jks
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // POST /containers/abc/start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
}

func TestPatchOnStartWithProxySocket(t *testing.T) {
	ctp, _ := NewContainerTruststorePatcher(CERT, ContainerTruststorePatcherOpts{RecursiveProxy: true})
	sock := tempSocketName(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	osHeader, osTar := statAndOpen(t, "os-release", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.16.0\n", "")
	certHeader, certTar := statAndOpen(t, "cert.pem", "", "")
	wants := []httpCall{
		// Ping daemon.
		{"/_ping", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Create container.
		{"/containers/create?name=abc", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		// Start container.
		{"/containers/abc/json", http.Response{StatusCode: http.StatusOK, Body: asBody([]byte(`{"Id": "def"}`))}},
		{"/containers/def/archive?path=/var/cache/proxy.crt", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/var/cache", http.Response{StatusCode: http.StatusOK}},
		{"/containers/def/archive?path=/kaniko", http.Response{StatusCode: http.StatusNotFound}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Header: osHeader}},
		{"/containers/def/archive?path=/etc/os-release", http.Response{StatusCode: http.StatusOK, Body: osTar}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Header: certHeader}},
		{"/containers/def/archive?path=/etc/ssl/cert.pem", http.Response{StatusCode: http.StatusOK, Body: certTar}},
		{"/containers/def/archive?path=/etc/ssl", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
		{"/containers/abc/start", http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	}
	outcome := make(chan error, 1)
	go fakeHTTPCalls(l, wants, outcome)
	// Ping daemon.
	clientIn := setupProxy(t, sock, ctp)
	req, err := http.NewRequest(http.MethodHead, "/_ping", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // HEAD /_ping
	resp, err := http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Create container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/create?name=abc", asBody([]byte(`{"HostConfig": {}}`)))
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // POST /containers/create?name=abc
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
	// Start container.
	clientIn = setupProxy(t, sock, ctp)
	req, err = http.NewRequest(http.MethodPost, "/containers/abc/start", http.NoBody)
	orFail(t, err)
	orFail(t, req.Write(clientIn))
	orFail(t, <-outcome) // GET /containers/abc/json
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/var/cache/proxy.crt
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/var/cache
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/os-release
	orFail(t, <-outcome) // HEAD /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // GET /containers/def/archive?path=/etc/ssl/cert.pem
	orFail(t, <-outcome) // PUT /containers/def/archive?path=/etc/ssl
	orFail(t, <-outcome) // POST /containers/abc/start
	resp, err = http.ReadResponse(bufio.NewReader(clientIn), req)
	orFail(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Unexpected return code: want=%d got=%d", http.StatusOK, resp.StatusCode)
	}
}

func TestAddBinding(t *testing.T) {
	for _, tc := range [](struct {
		Body []byte
		Src  string
		Dest string
		Mode string
		Want []byte
	}){
		{[]byte(`{"HostConfig":{}}`), "a", "b", "ro", []byte(`{"HostConfig":{"Binds":["a:b:ro"]}}`)},
		{[]byte(`{"HostConfig":{"Binds":[]}}`), "a", "b", "ro", []byte(`{"HostConfig":{"Binds":["a:b:ro"]}}`)},
		{[]byte(`{"HostConfig":{"Binds":["1:2:ro"]}}`), "a", "b", "ro", []byte(`{"HostConfig":{"Binds":["1:2:ro","a:b:ro"]}}`)},
	} {
		got, err := addBinding(tc.Body, tc.Src, tc.Dest, tc.Mode)
		if err != nil {
			t.Fatalf("addBinding(%s, %v, %v, %v): got=%s, want=%s", tc.Body, tc.Src, tc.Dest, tc.Mode, err, tc.Want)
		}
		if bytes.Compare(tc.Want, got) != 0 {
			t.Fatalf("addBinding(%s, %v, %v, %v): got=%s, want=%s", tc.Body, tc.Src, tc.Dest, tc.Mode, got, tc.Want)
		}
	}
}

func TestGetNetwork(t *testing.T) {
	testCases := []struct {
		name        string
		imageSpec   []byte
		wantNetwork string
		wantErr     bool
	}{
		{
			name:        "Valid network mode",
			imageSpec:   []byte(`{"HostConfig": {"NetworkMode": "bridge"}}`),
			wantNetwork: "bridge",
		},
		{
			name:      "Missing NetworkMode",
			imageSpec: []byte(`{"HostConfig": {}}`),
			wantErr:   true,
		},
		{
			name:      "Invalid JSON",
			imageSpec: []byte(`{"HostConfig": {`),
			wantErr:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotNetwork, err := getNetwork(tc.imageSpec)
			if (err != nil) != tc.wantErr {
				t.Errorf("getNetwork() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if gotNetwork != tc.wantNetwork {
				t.Errorf("getNetwork() = %v, want %v", gotNetwork, tc.wantNetwork)
			}
		})
	}
}

func TestSetNetwork(t *testing.T) {
	testCases := []struct {
		name       string
		imageSpec  []byte
		newNetwork string
		wantSpec   []byte
		wantErr    bool
	}{
		{
			name:       "Set new network",
			imageSpec:  []byte(`{"HostConfig": {"NetworkMode": "bridge"}}`),
			newNetwork: "host",
			wantSpec:   []byte(`{"HostConfig":{"NetworkMode":"host"}}`),
		},
		{
			name:       "Add network",
			imageSpec:  []byte(`{"HostConfig": {}}`),
			newNetwork: "host",
			wantSpec:   []byte(`{"HostConfig":{"NetworkMode":"host"}}`),
		},
		{
			name:       "Invalid JSON",
			imageSpec:  []byte(`{"HostConfig": {`),
			newNetwork: "host",
			wantErr:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotSpec, err := setNetwork(tc.imageSpec, tc.newNetwork)
			if (err != nil) != tc.wantErr {
				t.Errorf("setNetwork() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if tc.wantErr {
				return
			}
			if !bytes.Equal(gotSpec, tc.wantSpec) {
				t.Errorf("setNetwork() = %s, want %s", gotSpec, tc.wantSpec)
			}
		})
	}
}

func TestGetEnvVar(t *testing.T) {
	for _, tc := range [](struct {
		Body    []byte
		Var     string
		Want    string
		WantErr error
	}){
		{[]byte(`{}`), "FOO", "", fs.ErrNotExist},
		{[]byte(`{"Env":["BAR="]}`), "FOO", "", fs.ErrNotExist},
		{[]byte(`{"Env":["FOO="]}`), "FOO", "", nil},
		{[]byte(`{"Env":["FOO=a"]}`), "FOO", "a", nil},
		{[]byte(`{"Env":["FOO='a'"]}`), "FOO", "'a'", nil},
		{[]byte(`{"Env":["FOO='a'", "FOO='b'"]}`), "FOO", "'b'", nil},
	} {
		got, err := getEnvVar(tc.Body, tc.Var)
		if tc.WantErr != nil {
			if err.Error() != tc.WantErr.Error() {
				t.Fatalf("getEnvVar(%s, %v): got=%s, want=%s", tc.Body, tc.Var, err, tc.Want)
			}
		}
		if strings.Compare(tc.Want, got) != 0 {
			t.Fatalf("getEnvVars(%s, %v): got=%s, want=%s", tc.Body, tc.Var, got, tc.Want)
		}
	}
}

func TestAddEnvVars(t *testing.T) {
	for _, tc := range [](struct {
		Body []byte
		Vars []string
		Want []byte
	}){
		{[]byte(`{}`), []string{"FOO="}, []byte(`{"Env":["FOO="]}`)},
		{[]byte(`{"Env":null}`), []string{"FOO="}, []byte(`{"Env":["FOO="]}`)},
		{[]byte(`{"Env":[]}`), []string{"FOO="}, []byte(`{"Env":["FOO="]}`)},
		{[]byte(`{"Env":["BAR="]}`), []string{"FOO="}, []byte(`{"Env":["BAR=","FOO="]}`)},
		{[]byte(`{"Env":[]}`), []string{"FOO=", "BAR="}, []byte(`{"Env":["FOO=","BAR="]}`)},
		{[]byte(`{"Env":[]}`), []string{"FOO=foo"}, []byte(`{"Env":["FOO=foo"]}`)},
	} {
		got, err := addEnvVars(tc.Body, tc.Vars)
		if err != nil {
			t.Fatalf("addEnvVars(%s, %v): got=%s, want=%s", tc.Body, tc.Vars, err, tc.Want)
		}
		if bytes.Compare(tc.Want, got) != 0 {
			t.Fatalf("addEnvVars(%s, %v): got=%s, want=%s", tc.Body, tc.Vars, got, tc.Want)
		}
	}
}

func TestRemoveEnvVars(t *testing.T) {
	for _, tc := range [](struct {
		Body []byte
		Vars []string
		Want []byte
	}){
		{[]byte(`{}`), []string{"FOO"}, []byte(`{}`)},
		{[]byte(`{"Env":[]}`), []string{"FOO"}, []byte(`{"Env":[]}`)},
		{[]byte(`{"Env":["FOO="]}`), []string{"FOO"}, []byte(`{"Env":[]}`)},
		{[]byte(`{"Env":["FOO=","BAR="]}`), []string{"FOO"}, []byte(`{"Env":["BAR="]}`)},
		{[]byte(`{"Env":["BAR=","FOO="]}`), []string{"FOO"}, []byte(`{"Env":["BAR="]}`)},
		{[]byte(`{"Env":["BAR=","FOO="]}`), []string{"FOO", "BAR"}, []byte(`{"Env":[]}`)},
		{[]byte(`{"Env":["BAR=old","BAR=new","FOO=new"]}`), []string{"BAR", "FOO"}, []byte(`{"Env":["BAR=old"]}`)},
		{[]byte(`{"Env":["BAR=","BAZ=","FOO="]}`), []string{"BAR"}, []byte(`{"Env":["BAZ=","FOO="]}`)},
		{[]byte(`{"Env":["FOO=foo"]}`), []string{"FOO=foo"}, []byte(`{"Env":[]}`)},
		{[]byte(`{"Env":["FOO=foo"]}`), []string{"FOO=bar"}, []byte(`{"Env":["FOO=foo"]}`)},
		{[]byte(`{"Env":["FOO=old","FOO=new"]}`), []string{"FOO=old"}, []byte(`{"Env":["FOO=new"]}`)},
	} {
		got, err := removeEnvVars(tc.Body, tc.Vars)
		if err != nil {
			t.Fatalf("removeEnvVars(%s, %v): got=%s, want=%s", tc.Body, tc.Vars, err, tc.Want)
		}
		if bytes.Compare(tc.Want, got) != 0 {
			t.Fatalf("removeEnvVars(%s, %v): got=%s, want=%s", tc.Body, tc.Vars, got, tc.Want)
		}
	}
}
