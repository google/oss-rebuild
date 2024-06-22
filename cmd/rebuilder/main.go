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

// main contains the smoketest rebuilder, which triggers a rebuild local to this binary (not GCB).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/oss-rebuild/internal/api"
	gitinternal "github.com/google/oss-rebuild/internal/git"
	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/timewarp"
	rsrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	mavenrb "github.com/google/oss-rebuild/pkg/rebuild/maven"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
	"google.golang.org/grpc/codes"
)

var (
	debugBucket         = flag.String("debug-bucket", "", "if provided, the bucket to which rebuild results should be uploaded")
	gitCacheURL         = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
	defaultVersionCount = flag.Int("default-version-count", 5, "The number of versions to rebuild if no version is provided")
	useTimewarp         = flag.Bool("timewarp", true, "whether to use launch an instance of the timewarp server")
	timewarpPort        = flag.Int("timewarp-port", 8081, "the port for timewarp to serve on")
	localAssetDir       = flag.String("asset-dir", "assets", "the directory into which local assets will be stored")
)

var httpcfg = httpegress.Config{}

func doNpmRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		var err error
		req.Versions, err = npmrb.GetVersions(ctx, req.Package, mux)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		if len(req.Versions) > *defaultVersionCount {
			req.Versions = req.Versions[:*defaultVersionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "converting smoketest request to inputs")
	}
	return npmrb.RebuildMany(rbctx, inputs, mux)
}

func doPypiRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	m, err := mux.PyPI.Project(ctx, req.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to pypi metadata.")
	}
	if len(req.Versions) == 0 {
		req.Versions = make([]string, 0, len(m.Releases))
		for r := range m.Releases {
			req.Versions = append(req.Versions, r)
		}
		if len(req.Versions) > *defaultVersionCount {
			req.Versions = req.Versions[:*defaultVersionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "convert smoketest request to inputs")
	}
	return pypirb.RebuildMany(rbctx, inputs, mux)
}

func doCratesIORebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		var err error
		req.Versions, err = rsrb.GetVersions(ctx, req.Package, mux)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		if len(req.Versions) > *defaultVersionCount {
			req.Versions = req.Versions[:*defaultVersionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "converting smoketest request to inputs")
	}
	return rsrb.RebuildMany(rbctx, inputs, mux)
}

func doMavenRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		var meta mavenreg.MavenPackage
		meta, err := mavenreg.PackageMetadata(req.Package)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		req.Versions = meta.Versions
		if len(req.Versions) > *defaultVersionCount {
			req.Versions = req.Versions[:*defaultVersionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrapf(err, "converting smoketest request to inputs")
	}
	return mavenrb.RebuildMany(rbctx, req.Package, inputs)
}

type RebuildSmoketestDeps struct {
	HTTPClient  httpinternal.BasicClient
	GitCache    *gitinternal.Cache
	AssetDir    string
	TimewarpURL *string
	DebugBucket *string
	VersionStub api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func RebuildSmoketestInit(ctx context.Context) (*RebuildSmoketestDeps, error) {
	var d RebuildSmoketestDeps
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
		d.GitCache = &gitinternal.Cache{IDClient: c, APIClient: sc, URL: u}
	}
	if *useTimewarp {
		*d.TimewarpURL = fmt.Sprintf("localhost:%d", *timewarpPort)
	}
	if *debugBucket != "" {
		*d.DebugBucket = fmt.Sprintf("gs://%s", *debugBucket)
	}
	d.AssetDir = *localAssetDir
	return &d, nil
}

func RebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	log.Printf("Running smoketest: %v", sreq)
	ctx = context.WithValue(ctx, rebuild.RunID, sreq.ID)
	if deps.GitCache != nil {
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, *deps.GitCache)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: deps.HTTPClient},
		NPM:      npmreg.HTTPRegistry{Client: deps.HTTPClient},
		PyPI:     pypireg.HTTPRegistry{Client: deps.HTTPClient},
	}
	if deps.TimewarpURL != nil {
		ctx = context.WithValue(ctx, rebuild.TimewarpID, *deps.TimewarpURL)
	}
	ctx = context.WithValue(ctx, rebuild.AssetDirID, deps.AssetDir)
	if deps.DebugBucket != nil {
		ctx = context.WithValue(ctx, rebuild.UploadArtifactsPathID, *deps.DebugBucket)
	}
	var verdicts []rebuild.Verdict
	var err error
	switch sreq.Ecosystem {
	case rebuild.NPM:
		verdicts, err = doNpmRebuildSmoketest(ctx, sreq, mux)
	case rebuild.PyPI:
		verdicts, err = doPypiRebuildSmoketest(ctx, sreq, mux)
	case rebuild.CratesIO:
		verdicts, err = doCratesIORebuildSmoketest(ctx, sreq, mux)
	case rebuild.Maven:
		verdicts, err = doMavenRebuildSmoketest(ctx, sreq)
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	if err != nil {
		return nil, api.AsStatus(codes.Internal, err)
	}
	if len(verdicts) != len(sreq.Versions) {
		return nil, api.AsStatus(codes.Internal, errors.Errorf("unexpected number of results [want=%d,got=%d]", len(sreq.Versions), len(verdicts)))
	}
	smkVerdicts := make([]schema.Verdict, len(verdicts))
	for i, v := range verdicts {
		smkVerdicts[i] = schema.Verdict{
			Target:        v.Target,
			Message:       v.Message,
			StrategyOneof: schema.NewStrategyOneOf(v.Strategy),
			Timings:       v.Timings,
		}
	}
	return &schema.SmoketestResponse{Verdicts: smkVerdicts, Executor: os.Getenv("K_REVISION")}, nil
}

func sanitize(key string) string {
	return strings.ReplaceAll(key, "/", "!")
}

func Version(ctx context.Context, req schema.VersionRequest, _ *api.NoDeps) (*schema.VersionResponse, error) {
	return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
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
	http.HandleFunc("/smoketest", api.Handler(RebuildSmoketestInit, RebuildSmoketest))
	http.HandleFunc("/version", api.Handler(api.NoDepsInit, Version))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
