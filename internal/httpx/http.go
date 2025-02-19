// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package http provides a simpler http.Client abstraction and derivative uses.
package httpx

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/google/oss-rebuild/internal/cache"
)

// BasicClient is a simpler http.Client that only requires a Do method.
type BasicClient interface {
	Do(*http.Request) (*http.Response, error)
}

var _ BasicClient = http.DefaultClient

// WithUserAgent is a basic HTTP client that adds a User-Agent header.
type WithUserAgent struct {
	BasicClient
	UserAgent string
}

var _ BasicClient = &WithUserAgent{}

// Do adds the User-Agent header and sends the request.
func (c *WithUserAgent) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", c.UserAgent)
	return c.BasicClient.Do(req)
}

// CachedClient is a BasicClient that caches responses.
type CachedClient struct {
	BasicClient
	ch cache.Cache
}

// NewCachedClient returns a new CachedClient.
func NewCachedClient(client BasicClient, c cache.Cache) *CachedClient {
	return &CachedClient{client, c}
}

// Do attempts to fetch from cache (if applicable) or fulfills the request using the underlying client.
func (cc *CachedClient) Do(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return cc.BasicClient.Do(req)
	}
	respBytes, err := cc.ch.GetOrSet(req.URL.String(), func() (any, error) {
		resp, err := cc.BasicClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New(resp.Status)
		}
		defer resp.Body.Close()
		foo := new(bytes.Buffer)
		if err := resp.Write(foo); err != nil {
			return nil, err
		}
		return foo.Bytes(), nil
	})
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(respBytes.([]byte))), req)
}

var _ BasicClient = &CachedClient{}

type RateLimitedClient struct {
	BasicClient
	Ticker *time.Ticker
}

func (c *RateLimitedClient) Do(req *http.Request) (*http.Response, error) {
	<-c.Ticker.C // Wait for next tick
	return c.BasicClient.Do(req)
}

var _ BasicClient = &RateLimitedClient{}
