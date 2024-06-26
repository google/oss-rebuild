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

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
)

var (
	gitCacheURL = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
)

var httpcfg = httpegress.Config{}

func InferInit(ctx context.Context) (*inferenceservice.InferDeps, error) {
	var d inferenceservice.InferDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	if *gitCacheURL != "" {
		c, err := idtoken.NewClient(ctx, *gitCacheURL)
		if err != nil {
			return nil, errors.Wrap(err, "creating git cache id client")
		}
		sc, _, err := gapihttp.NewClient(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating git cache API client")
		}
		u, err := url.Parse(*gitCacheURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing git cache URL")
		}
		d.GitCache = &gitx.Cache{IDClient: c, APIClient: sc, URL: u}
	}
	return &d, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/infer", api.Handler(InferInit, inferenceservice.Infer))
	http.HandleFunc("/version", api.Handler(api.NoDepsInit, inferenceservice.Version))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
