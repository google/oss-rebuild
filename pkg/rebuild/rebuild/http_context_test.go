// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type contextKey struct{}

type contextCapturingClient struct {
	ctx context.Context
}

func (c *contextCapturingClient) Do(req *http.Request) (*http.Response, error) {
	c.ctx = req.Context()
	return nil, errors.New("stop after capture")
}

func TestDoContextAttachesContextToRequest(t *testing.T) {
	client := &contextCapturingClient{}
	ctx := context.WithValue(context.Background(), contextKey{}, "attached")
	ctx = context.WithValue(ctx, HTTPBasicClientID, client)
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DoContext(ctx, req); err == nil {
		t.Fatal("DoContext() error = nil, want capture sentinel")
	}
	if got := client.ctx.Value(contextKey{}); got != "attached" {
		t.Fatalf("request context value = %v, want attached", got)
	}
}
