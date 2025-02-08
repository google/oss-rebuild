// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package httpegress provides a client constructor for building an HTTP Client for making requests to external services.
package httpegress

import (
	"context"
	"flag"
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/gateway"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
)

// Config is the configuration for building an HTTP egress client.
type Config struct {
	GatewayURL string
	UserAgent  string
}

// RegisterFlags registers the flags for building an HTTP egress client.
func (cfg *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&cfg.GatewayURL, "gateway-url", "", "if provided, the gateway service to use to access external HTTP APIs")
	fs.StringVar(&cfg.UserAgent, "user-agent", "", "if provided, the User-Agent string that will be used to contact external HTTP APIs")
}

// MakeClient creates a new HTTP BasicClient for making egress requests.
func MakeClient(ctx context.Context, cfg Config) (httpx.BasicClient, error) {
	var client httpx.BasicClient
	if cfg.UserAgent != "" {
		client = &httpx.WithUserAgent{BasicClient: http.DefaultClient, UserAgent: cfg.UserAgent}
	} else {
		client = http.DefaultClient
	}
	if cfg.GatewayURL != "" {
		c, err := idtoken.NewClient(ctx, cfg.GatewayURL)
		if err != nil {
			return nil, errors.Wrap(err, "initializing identity client")
		}
		u, err := url.Parse(cfg.GatewayURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing gateway URL")
		}
		return &gateway.Client{RedirectClient: client, IDClient: c, URL: u}, nil
	}
	return client, nil
}
