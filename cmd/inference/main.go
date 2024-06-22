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
	"os"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/api"
	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

var httpcfg = httpegress.Config{}

func doInfer(ctx context.Context, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux, hint rebuild.Strategy) (rebuild.Strategy, error) {
	s := memory.NewStorage()
	fs := memfs.New()
	var repo string
	if lh, ok := hint.(*rebuild.LocationHint); ok && lh != nil {
		repo = lh.Location.Repo
	} else {
		var err error
		repo, err = rebuilder.InferRepo(ctx, t, mux)
		if err != nil {
			return nil, err
		}
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, fs, s)
	if err != nil {
		return nil, err
	}
	strategy, err := rebuilder.InferStrategy(ctx, t, mux, &rcfg, hint)
	if err != nil {
		return nil, err
	}
	return strategy, nil
}

type InferDeps struct {
	HTTPClient httpinternal.BasicClient
}

func InferInit(ctx context.Context) (*InferDeps, error) {
	var d InferDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	return &d, nil
}

func Infer(ctx context.Context, req schema.InferenceRequest, deps *InferDeps) (*schema.StrategyOneOf, error) {
	if req.LocationHint() != nil && req.LocationHint().Ref == "" && req.LocationHint().Dir != "" {
		return nil, api.AsStatus(codes.Unimplemented, errors.New("location hint dir without ref not implemented"))
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: deps.HTTPClient},
		NPM:      npmreg.HTTPRegistry{Client: deps.HTTPClient},
		PyPI:     pypireg.HTTPRegistry{Client: deps.HTTPClient},
	}
	var s rebuild.Strategy
	t := rebuild.Target{
		Ecosystem: req.Ecosystem,
		Package:   req.Package,
		Version:   req.Version,
	}
	// TODO: Use req.LocationHint in these individual infer calls.
	var err error
	switch req.Ecosystem {
	case rebuild.NPM:
		s, err = doInfer(ctx, npm.Rebuilder{}, t, mux, req.LocationHint())
	case rebuild.PyPI:
		s, err = doInfer(ctx, pypi.Rebuilder{}, t, mux, req.LocationHint())
	case rebuild.CratesIO:
		s, err = doInfer(ctx, cratesio.Rebuilder{}, t, mux, req.LocationHint())
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	if err != nil {
		log.Printf("No inference for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, err)
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("no inference provided"))
	}
	oneof := schema.NewStrategyOneOf(s)
	return &oneof, nil
}

func Version(ctx context.Context, req schema.VersionRequest, _ *api.NoDeps) (*schema.VersionResponse, error) {
	return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/infer", api.Handler(InferInit, Infer))
	http.HandleFunc("/version", api.Handler(api.NoDepsInit, Version))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
