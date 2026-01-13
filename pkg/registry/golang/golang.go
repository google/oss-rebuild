// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package golang

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
)

var proxyURL = urlx.MustParse("https://proxy.golang.org")

// Registry is a Go module registry.
type Registry interface {
	// Module fetches the .zip archive for a module.
	Module(ctx context.Context, pkg, version string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the proxy.golang.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Module fetches the .zip archive for a module from proxy.golang.org.
func (r HTTPRegistry) Module(ctx context.Context, pkg, version string) (io.ReadCloser, error) {
	pathURL, err := url.Parse(path.Join(pkg, "@v", version+".zip"))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", proxyURL.ResolveReference(pathURL).String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return resp.Body, nil
}
