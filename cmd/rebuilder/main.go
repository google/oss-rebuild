// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// main contains the smoketest rebuilder, which triggers a rebuild local to this binary (not GCB).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/rebuilderservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/timewarp"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
)

var (
	debugStorage        = flag.String("debug-storage", "", "if provided, the location in which rebuild debug info should be stored")
	gitCacheURL         = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
	defaultVersionCount = flag.Int("default-version-count", 5, "The number of versions to rebuild if no version is provided")
	useTimewarp         = flag.Bool("timewarp", true, "whether to use launch an instance of the timewarp server")
	timewarpPort        = flag.Int("timewarp-port", 8081, "the port for timewarp to serve on")
	localAssetDir       = flag.String("asset-dir", "assets", "the directory into which local assets will be stored")
)

var httpcfg = httpegress.Config{}

func RebuildSmoketestInit(ctx context.Context) (*rebuilderservice.RebuildSmoketestDeps, error) {
	var d rebuilderservice.RebuildSmoketestDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating http client")
	}
	if *gitCacheURL != "" {
		c, err := idtoken.NewClient(ctx, *gitCacheURL)
		if err != nil {
			return nil, errors.Wrap(err, "creating id client")
		}
		sc, _, err := gapihttp.NewClient(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating api client")
		}
		u, err := url.Parse(*gitCacheURL)
		if err != nil {
			log.Fatalf("Failed to create API Client: %v", err)
		}
		d.GitCache = &gitx.Cache{IDClient: c, APIClient: sc, URL: u}
	}
	if *useTimewarp {
		addr := fmt.Sprintf("localhost:%d", *timewarpPort)
		d.TimewarpURL = &addr
	}
	if *debugStorage != "" {
		d.DebugStorage = debugStorage
	}
	d.AssetDir = *localAssetDir
	d.DefaultVersionCount = *defaultVersionCount
	return &d, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if *useTimewarp {
		go func() {
			if err := http.ListenAndServe(fmt.Sprintf(":%d", *timewarpPort), timewarp.Handler{}); err != nil {
				log.Fatalln(err)
			}
		}()
	}
	http.HandleFunc("/smoketest", api.Handler(RebuildSmoketestInit, rebuilderservice.RebuildSmoketest))
	http.HandleFunc("/version", api.Handler(api.NoDepsInit, rebuilderservice.Version))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
