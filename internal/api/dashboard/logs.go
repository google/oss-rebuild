// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

const MAX_LOG_SIZE_BYTES = 10 * 1024 * 1024

var _ api.HandlerT[LogsRequest, LogsData, *Deps] = Logs

type LogsRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string
}

func (LogsRequest) Validate() error { return nil }

type LogLine struct {
	Number int
	Text   string
}

type LogsData struct {
	Target      rebuild.Target
	RunID       string
	RedirectURL string // If populdated, render a redirection popup.
	Lines       []LogLine
}

func Logs(ctx context.Context, req LogsRequest, deps *Deps) (*LogsData, error) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		return nil, fmt.Errorf("log viewing is not configured")
	}

	target := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}
	obj, attrs, err := fetchLogObject(ctx, req, deps)
	if err != nil {
		return nil, err
	}

	if attrs.Size > MAX_LOG_SIZE_BYTES {
		return &LogsData{
			Target:      target,
			RunID:       req.RunID,
			RedirectURL: "raw/",
		}, nil
	}

	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating log reader: %w", err)
	}
	defer reader.Close()

	var lines []LogLine
	scanner := bufio.NewScanner(reader)
	lineNumber := 1
	for scanner.Scan() {
		lines = append(lines, LogLine{
			Number: lineNumber,
			Text:   scanner.Text(),
		})
		lineNumber++
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning logs: %v", err)
	}

	return &LogsData{
		Target: target,
		RunID:  req.RunID,
		Lines:  lines,
	}, nil
}

func fetchLogObject(ctx context.Context, req LogsRequest, deps *Deps) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching attempt: %w", err)
	}

	logID := attempt.BuildID
	if logID == "" {
		logID = attempt.ObliviousID
	}
	if logID == "" {
		return nil, nil, fmt.Errorf("no logs available for this attempt")
	}

	obj := deps.GCSClient.Bucket(deps.LogsBucket).Object(gcb.MergedLogFile(logID))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching log attributes: %w", err)
	}

	return obj, attrs, nil
}

func HandleRawLogs(w http.ResponseWriter, r *http.Request, req LogsRequest, deps *Deps) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		http.Error(w, "Log viewing is not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	obj, _, err := fetchLogObject(ctx, req, deps)
	if err != nil {
		log.Printf("Error fetching logs: %v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	reader, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Error creating log reader: %v", err)
		http.Error(w, "Failed to create log reader", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("Error streaming logs: %v", err)
	}
}
