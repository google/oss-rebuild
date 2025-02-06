// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuilderservice

import (
	"context"
	"log"
	"os"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpx"
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	debianrb "github.com/google/oss-rebuild/pkg/rebuild/debian"
	mavenrb "github.com/google/oss-rebuild/pkg/rebuild/maven"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

func doDebianRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux, versionCount int) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		return nil, errors.New("Debian smoketest versions must not be empty")
	}
	rbctx := ctx
	// TODO: Require artifacts and populate those for debian.
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "convert smoketest request to inputs")
	}
	return debianrb.RebuildMany(rbctx, inputs, mux)
}

func doNpmRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux, versionCount int) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		var err error
		req.Versions, err = npmrb.GetVersions(ctx, req.Package, mux)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		if len(req.Versions) > versionCount {
			req.Versions = req.Versions[:versionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "converting smoketest request to inputs")
	}
	return npmrb.RebuildMany(rbctx, inputs, mux)
}

func doPypiRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux, versionCount int) ([]rebuild.Verdict, error) {
	m, err := mux.PyPI.Project(ctx, req.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to pypi metadata.")
	}
	if len(req.Versions) == 0 {
		req.Versions = make([]string, 0, len(m.Releases))
		for r := range m.Releases {
			req.Versions = append(req.Versions, r)
		}
		if len(req.Versions) > versionCount {
			req.Versions = req.Versions[:versionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "convert smoketest request to inputs")
	}
	return pypirb.RebuildMany(rbctx, inputs, mux)
}

func doCratesIORebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux, versionCount int) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		var err error
		req.Versions, err = cratesrb.GetVersions(ctx, req.Package, mux)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		if len(req.Versions) > versionCount {
			req.Versions = req.Versions[:versionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrap(err, "converting smoketest request to inputs")
	}
	return cratesrb.RebuildMany(rbctx, inputs, mux)
}

func doMavenRebuildSmoketest(ctx context.Context, req schema.SmoketestRequest, mux rebuild.RegistryMux, versionCount int) ([]rebuild.Verdict, error) {
	if len(req.Versions) == 0 {
		meta, err := mux.Maven.PackageMetadata(ctx, req.Package)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch versions")
		}
		req.Versions = meta.Versions
		if len(req.Versions) > versionCount {
			req.Versions = req.Versions[:versionCount]
		}
	}
	rbctx := ctx
	inputs, err := req.ToInputs()
	if err != nil {
		return nil, errors.Wrapf(err, "converting smoketest request to inputs")
	}
	return mavenrb.RebuildMany(rbctx, inputs, mux)
}

type RebuildSmoketestDeps struct {
	HTTPClient          httpx.BasicClient
	GitCache            *gitx.Cache
	AssetDir            string
	TimewarpURL         *string
	DebugStorage        *string
	DefaultVersionCount int
}

func RebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	log.Printf("Running smoketest: %v", sreq)
	ctx = context.WithValue(ctx, rebuild.RunID, sreq.ID)
	if deps.GitCache != nil {
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, *deps.GitCache)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := rebuild.RegistryMux{
		Debian:   debianreg.HTTPRegistry{Client: deps.HTTPClient},
		CratesIO: cratesreg.HTTPRegistry{Client: deps.HTTPClient},
		NPM:      npmreg.HTTPRegistry{Client: deps.HTTPClient},
		PyPI:     pypireg.HTTPRegistry{Client: deps.HTTPClient},
		Maven:    mavenreg.HTTPRegistry{Client: deps.HTTPClient},
	}
	if deps.TimewarpURL != nil {
		ctx = context.WithValue(ctx, rebuild.TimewarpID, *deps.TimewarpURL)
	}
	ctx = context.WithValue(ctx, rebuild.AssetDirID, deps.AssetDir)
	if deps.DebugStorage != nil {
		ctx = context.WithValue(ctx, rebuild.DebugStoreID, *deps.DebugStorage)
	}
	var verdicts []rebuild.Verdict
	var err error
	switch sreq.Ecosystem {
	case rebuild.Debian:
		verdicts, err = doDebianRebuildSmoketest(ctx, sreq, mux, deps.DefaultVersionCount)
	case rebuild.NPM:
		verdicts, err = doNpmRebuildSmoketest(ctx, sreq, mux, deps.DefaultVersionCount)
	case rebuild.PyPI:
		verdicts, err = doPypiRebuildSmoketest(ctx, sreq, mux, deps.DefaultVersionCount)
	case rebuild.CratesIO:
		verdicts, err = doCratesIORebuildSmoketest(ctx, sreq, mux, deps.DefaultVersionCount)
	case rebuild.Maven:
		verdicts, err = doMavenRebuildSmoketest(ctx, sreq, mux, deps.DefaultVersionCount)
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
