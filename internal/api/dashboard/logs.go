// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var _ api.HandlerT[LogsRequest, LogsView, *Deps] = Logs

type LogsRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string
}

func (LogsRequest) Validate() error { return nil }

type LogsView struct {
	Target rebuild.Target
	Logs   string
}

func Logs(ctx context.Context, req LogsRequest, deps *Deps) (*LogsView, error) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		return nil, errors.New("Log viewing is not configured")
	}

	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, errors.Wrap(err, "fetching attempt")
	}

	logID := attempt.BuildID
	if logID == "" {
		logID = attempt.ObliviousID
	}
	if logID == "" {
		return nil, errors.New("no logs available for this attempt")
	}

	obj := deps.GCSClient.Bucket(deps.LogsBucket).Object(gcb.MergedLogFile(logID))
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetching logs")
	}
	defer reader.Close()

	b, err := io.ReadAll(reader)
	if err != nil {
		return nil, errors.Wrap(err, "reading logs")
	}

	t := attempt.Target()

	return &LogsView{
		Target: t,
		Logs:   string(b),
	}, nil
}
