// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[AttemptRequest, AttemptData, *Deps] = Attempt

type AttemptRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string `form:"runid"`
}

func (AttemptRequest) Validate() error { return nil }

type AttemptData struct {
	Frame
	Ecosystem       string
	PackageName     string
	EncodedPackage  string
	Attempt         *RebuildView
	AttemptStrategy string
	AttemptDuration string
	LogsURL         string // the build log in the GCS console
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
	var finished time.Time
	if !attempt.Finished.IsZero() {
		finished = attempt.Finished
	} else {
		finished = attempt.Created
	}
	if !attempt.Started.IsZero() && !finished.IsZero() {
		durationStr = finished.Sub(attempt.Started).Round(time.Second).String()
	}

	return &AttemptData{
		Attempt:         new(NewRebuildView(attempt)),
		AttemptStrategy: string(strategyBytes),
		AttemptDuration: durationStr,
		LogsURL:         logsURL(deps.LogsBucket, attempt),
	}, nil
}

// logsURL links an attempt's build log in the GCS console.
func logsURL(bucket string, attempt rundex.Rebuild) string {
	id := cmp.Or(attempt.BuildID, attempt.ObliviousID)
	if bucket == "" || id == "" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: "storage.cloud.google.com", Path: "/" + bucket + "/" + gcb.MergedLogFile(id)}).String()
}
