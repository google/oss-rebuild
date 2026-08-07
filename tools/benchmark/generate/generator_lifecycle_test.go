// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type countingBody struct {
	io.Reader
	closed *atomic.Int64
}

func (b countingBody) Close() error {
	b.closed.Add(1)
	return nil
}

type cratesRoundTripper struct {
	pageRequests atomic.Int64
	pageClosed   atomic.Int64
	cancel       context.CancelFunc
}

func (rt *cratesRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.cancel != nil {
		rt.cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	var body string
	var responseBody io.ReadCloser
	if req.URL.Path == "/api/v1/crates" {
		rt.pageRequests.Add(1)
		page, err := strconv.Atoi(req.URL.Query().Get("page"))
		if err != nil {
			return nil, err
		}
		var items []string
		for i := 0; i < 100; i++ {
			items = append(items, fmt.Sprintf(`{"id":"crate-%d-%d"}`, page, i))
		}
		body = `{"crates":[` + strings.Join(items, ",") + `]}`
		responseBody = countingBody{Reader: strings.NewReader(body), closed: &rt.pageClosed}
	} else {
		name := strings.TrimPrefix(req.URL.Path, "/api/v1/crates/")
		body = fmt.Sprintf(`{"crate":{"id":%q},"versions":[{"num":"1.0.0","created_at":"2026-08-01T00:00:00Z"}]}`, name)
		responseBody = io.NopCloser(strings.NewReader(body))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       responseBody,
		Request:    req,
	}, nil
}

func TestCratesIOGeneratorClosesResponses(t *testing.T) {
	rt := &cratesRoundTripper{}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: rt}
	defer func() { http.DefaultClient = oldClient }()

	got := cratesioTop2000.Generator(t.Context())
	if len(got.Packages) != maxPackages {
		t.Fatalf("packages = %d, want %d", len(got.Packages), maxPackages)
	}
	if got.Count != maxPackages {
		t.Fatalf("count = %d, want %d", got.Count, maxPackages)
	}
	if got, want := rt.pageClosed.Load(), rt.pageRequests.Load(); got != want {
		t.Fatalf("closed page responses = %d, want %d", got, want)
	}
}

func TestCratesIOGeneratorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	rt := &cratesRoundTripper{cancel: cancel}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: rt}
	defer func() { http.DefaultClient = oldClient }()

	got := cratesioTop2000.Generator(ctx)
	if len(got.Packages) != 0 {
		t.Fatalf("packages = %d, want 0", len(got.Packages))
	}
}
