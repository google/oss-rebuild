// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package gateway provides a client for the gateway service.
package gateway

import (
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

// Client is a client for the gateway service.
type Client struct {
	RedirectClient httpx.BasicClient
	IDClient       *http.Client
	URL            *url.URL
}

var _ httpx.BasicClient = &Client{}

// Do sends a request to the gateway service and then through to the actual endpoint.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	u, err := c.URL.Parse("/")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Add("uri", req.URL.String())
	u.RawQuery = q.Encode()
	c.IDClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		// Never follow redirect since the successful response will be a 302.
		return http.ErrUseLastResponse
	}
	resp, err := c.IDClient.Get(u.String())
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusFound:
		return c.RedirectClient.Do(req)
	case http.StatusBadRequest:
		log.Println(io.ReadAll(resp.Body))
		return nil, errors.New("gateway request rejected")
	default:
		return nil, errors.Wrap(errors.New(resp.Status), "requesting gateway")
	}
}
