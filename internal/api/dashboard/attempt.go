// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var _ api.HandlerT[AttemptRequest, AttemptData, *Deps] = Attempt

type AttemptRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string `form:"runid"`
}

func (AttemptRequest) Validate() error { return nil }

type AttemptData struct {
	Ecosystem       string
	PackageName     string
	EncodedPackage  string
	Attempt         *RebuildView
	AttemptStrategy string
	AttemptDuration string
}

func Attempt(ctx context.Context, req AttemptRequest, deps *Deps) (*AttemptData, error) {
	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, errors.Wrap(err, "fetching attempt")
	}

	attempts := []rundex.Rebuild{attempt}
	applySuccessRegex(deps.SuccessRegex, attempts)
	attempt = attempts[0]

	strategyBytes, _ := json.MarshalIndent(attempt.Strategy, "", "  ")

	durationStr := "N/A"
	if !attempt.Started.IsZero() && !attempt.Created.IsZero() {
		durationStr = attempt.Created.Sub(attempt.Started).Round(time.Second).String()
	}

	view := NewRebuildView(attempt)
	return &AttemptData{
		Attempt:         &view,
		AttemptStrategy: string(strategyBytes),
		AttemptDuration: durationStr,
	}, nil
}
