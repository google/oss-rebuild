// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
		return nil, errors.Errorf("Request failed: %s", resp.Status)
	}
}
