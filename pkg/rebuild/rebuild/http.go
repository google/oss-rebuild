// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"net/http"

	"github.com/google/oss-rebuild/internal/httpx"
)

// DoContext attempts to make an external HTTP request using the gateway client, if configured.
func DoContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c, ok := ctx.Value(HTTPBasicClientID).(httpx.BasicClient); ok && c != nil {
		return c.Do(req)
	}
	return http.DefaultClient.Do(req)
}
