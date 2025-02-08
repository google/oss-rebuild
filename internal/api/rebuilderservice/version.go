// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuilderservice

import (
	"context"
	"os"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func Version(ctx context.Context, req schema.VersionRequest, _ *api.NoDeps) (*schema.VersionResponse, error) {
	return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
}
