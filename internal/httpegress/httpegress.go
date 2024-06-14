// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package httpegress provides a client constructor for building an HTTP Client for making requests to external services.
package httpegress

import (
	"context"
	"flag"
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/gateway"
	httpinternal "github.com/google/oss-rebuild/internal/http"
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
func MakeClient(ctx context.Context, cfg Config) (httpinternal.BasicClient, error) {
	var client httpinternal.BasicClient
	if cfg.UserAgent != "" {
		client = &httpinternal.WithUserAgent{BasicClient: http.DefaultClient, UserAgent: cfg.UserAgent}
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
