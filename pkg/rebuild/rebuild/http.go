// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/oss-rebuild/internal/buildinfo"
	"github.com/google/oss-rebuild/internal/httpx"
)

// defaultVersion is used in the User-Agent when the build is unstamped
// (local `go build`, tests, tooling) and buildinfo.Version is empty.
const defaultVersion = "0.0.0"

// UserAgent returns the User-Agent string for outbound registry requests
// originating from the named host. host is a deployment name (var.host) or
// "localbuild" for anonymous local/interactive traffic. The version is the
// link-time build identity from buildinfo, falling back to defaultVersion.
func UserAgent(host string) string {
	version := buildinfo.Version
	if version == "" {
		version = defaultVersion
	}
	return fmt.Sprintf("oss-rebuild+%s/%s", host, version)
}

// DoContext attempts to make an external HTTP request using the gateway client, if configured.
func DoContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c, ok := ctx.Value(HTTPBasicClientID).(httpx.BasicClient); ok && c != nil {
		return c.Do(req)
	}
	// No configured client (local/tooling paths): stamp an anonymous
	// User-Agent so the fallback isn't UA-less. A configured client applies
	// its own via httpx.WithUserAgent.
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent("localbuild"))
	}
	return http.DefaultClient.Do(req)
}
