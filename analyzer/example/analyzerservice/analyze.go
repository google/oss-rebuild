// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"context"
	"net/url"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type AnalyzerDeps struct {
	BuildRepo    *url.URL
	BuildVersion string
	Findings     rebuild.LocatableAssetStore
}

func Analyze(ctx context.Context, e schema.AnalyzeRebuildRequest, deps *AnalyzerDeps) (*api.NoReturn, error) {
	// =============================================
	// ========== Implement analysis here ==========
	// =============================================
	return nil, nil
}
