// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
)

// UDSHTTPClient is a client that routes all requests to a UNIX Domain Socket.
type UDSHTTPClient struct {
	*http.Client
}

var _ httpx.BasicClient = &UDSHTTPClient{}

// NewUDSHTTPClient returns a new UDS HTTP client.
func NewUDSHTTPClient(socket string) *UDSHTTPClient {
	return &UDSHTTPClient{&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			return net.Dial("unix", socket)
		},
		// Avoid using HTTP/2 or environment Proxy settings.
		Proxy:             nil,
		ForceAttemptHTTP2: false,
		// Settings from DefaultTransport.
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}}}
}

// Do executes a HTTP Request routed through the UDS socket.
func (c *UDSHTTPClient) Do(req *http.Request) (resp *http.Response, err error) {
	// NOTE: http.Client requires a valid URL. This is safe because the Docker
	// socket ignores all URL elements besides the Path.
	req.URL.Scheme = "http"
	req.URL.Host = "google.com"
	return c.Client.Do(req)
}

// Get executes a HTTP GET Request routed through the UDS socket.
func (c *UDSHTTPClient) Get(url string) (resp *http.Response, err error) {
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post executes a HTTP POST Request routed through the UDS socket.
func (c *UDSHTTPClient) Post(url, contentType string, body io.Reader) (resp *http.Response, err error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// PostForm executes a HTTP POST Request routed through the UDS socket.
func (c *UDSHTTPClient) PostForm(url string, data url.Values) (resp *http.Response, err error) {
	return nil, errors.New("Unsupported operation")
}

// Head executes a HTTP HEAD Request routed through the UDS socket.
func (c *UDSHTTPClient) Head(url string) (resp *http.Response, err error) {
	return nil, errors.New("Unsupported operation")
}
