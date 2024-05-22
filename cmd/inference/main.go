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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/pkg/errors"
	"github.com/go-git/go-git/v5/storage/memory"
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
		repo, err = rebuilder.InferRepo(t, mux)
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

func HandleInfer(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	ireq, err := schema.NewInferenceRequest(req.Form)
	if err != nil {
		log.Println(errors.Wrap(err, "parsing inference request"))
		http.Error(rw, err.Error(), 400)
		return
	}
	if ireq.LocationHint != nil && ireq.LocationHint.Ref == "" && ireq.LocationHint.Dir != "" {
		http.Error(rw, fmt.Sprintf("A location hint dir without ref is not yet supported. Received: %v", *ireq.LocationHint), 400)
		return
	}
	client, err := httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		log.Fatalf("Failed to initialize HTTP egress client: %v", err)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, client)
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: client},
		NPM:      npmreg.HTTPRegistry{Client: client},
		PyPI:     pypireg.HTTPRegistry{Client: client},
	}
	var s rebuild.Strategy
	t := rebuild.Target{
		Ecosystem: ireq.Ecosystem,
		Package:   ireq.Package,
		Version:   ireq.Version,
	}
	// TODO: Use ireq.LocationHint in these individual infer calls.
	switch ireq.Ecosystem {
	case rebuild.NPM:
		s, err = doInfer(ctx, npm.Rebuilder{}, t, mux, ireq.LocationHint)
	case rebuild.PyPI:
		s, err = doInfer(ctx, pypi.Rebuilder{}, t, mux, ireq.LocationHint)
	case rebuild.CratesIO:
		s, err = doInfer(ctx, cratesio.Rebuilder{}, t, mux, ireq.LocationHint)
	default:
		http.Error(rw, "unsupported ecosystem", 400)
		return
	}
	if err != nil {
		log.Printf("No inference for [pkg=%s, version=%v]: %v\n", ireq.Package, ireq.Version, err)
		http.Error(rw, "No inference provided", 400)
		return
	}
	enc := json.NewEncoder(rw)
	if err := enc.Encode(schema.NewStrategyOneOf(s)); err != nil {
		log.Printf("Failed to encode verdicts for [pkg=%s, version=%v]: %v\n", ireq.Package, ireq.Version, err)
		http.Error(rw, "Encoding error", 500)
	}
	return
}

func HandleVersion(rw http.ResponseWriter, req *http.Request) {
	rw.Write([]byte(os.Getenv("K_REVISION")))
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/infer", HandleInfer)
	http.HandleFunc("/version", HandleVersion)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
