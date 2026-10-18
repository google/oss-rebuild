// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpegress

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// probeURL is outside Google's network, so like registries and code hosts it
// goes through the VPC's NAT rather than Private Google Access.
const probeURL = "https://github.com/"

// grace bounds the wait on the internet so a probe host outage can't block new instances or jobs.
const grace = 60 * time.Second

var (
	started = time.Now()
	reached atomic.Bool
	lapsed  sync.Once
)

// ready returns nil once c reaches probeURL or grace has passed since the
// process started.
func ready(ctx context.Context, c *http.Client) error {
	if time.Since(started) >= grace {
		if !reached.Load() {
			lapsed.Do(func() { log.Printf("Proceeding without reaching %s in %v", probeURL, grace) })
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodHead, probeURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	reached.Store(true)
	return nil
}

// ReadyHandler serves a startup probe, passing once ready with c returns nil.
func ReadyHandler(c *http.Client) http.HandlerFunc {
	return func(rw http.ResponseWriter, req *http.Request) {
		if err := ready(req.Context(), c); err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	}
}

// AwaitReady blocks until ready returns nil or ctx ends. A job has no startup
// probe to hold it, so it waits before its first request leaves the VPC.
func AwaitReady(ctx context.Context, c *http.Client) {
	for ready(ctx, c) != nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
