// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"fmt"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type RebuildSmoketestDeps struct {
	FirestoreClient *firestore.Client
	SmoketestStub   api.StubT[schema.SmoketestRequest, schema.SmoketestResponse]
	VersionStub     api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func rebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	log.Printf("Dispatching smoketest: %v", sreq)
	versionChan := make(chan string, 1)
	go func() {
		resp, err := deps.VersionStub(ctx, schema.VersionRequest{Service: "build-local"})
		if err != nil {
			log.Println(errors.Wrap(err, "making version request"))
			versionChan <- "unknown"
		} else {
			versionChan <- resp.Version
		}
		close(versionChan)
	}()
	stubresp, stuberr := deps.SmoketestStub(ctx, sreq)
	switch {
	case stuberr == nil:
		return stubresp, nil
	case !errors.Is(stuberr, api.ErrNotOK):
		return nil, api.AsStatus(codes.Internal, errors.Wrap(stuberr, "making smoketest request"))
	default:
		var resp schema.SmoketestResponse
		log.Printf("smoketest failed: %v\n", stuberr)
		// If smoketest failed, populate the verdicts with as much info as we can (pulling executor
		// version from the smoketest version endpoint if possible.
		resp.Executor = <-versionChan
		for _, v := range sreq.Versions {
			resp.Verdicts = append(resp.Verdicts, schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(sreq.Ecosystem),
					Package:   sreq.Package,
					Version:   v,
					// TODO: This should be populated by the sreq when we support different artifact types.
					Artifact: "",
				},
				Message: fmt.Sprintf("build-local failed: %v", stuberr),
			})
		}
		return &resp, nil
	}
}
func RebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	started := time.Now().UTC()
	if sreq.ID == "" {
		sreq.ID = started.Format(time.RFC3339)
	}
	resp, err := rebuildSmoketest(ctx, sreq, deps)
	for _, v := range resp.Verdicts {
		_, err := deps.FirestoreClient.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(sreq.Package)).Collection("versions").Doc(v.Target.Version).Collection("artifacts").Doc(v.Target.Artifact).Collection("attempts").Doc(sreq.ID).Set(ctx, schema.RebuildAttempt{
			Ecosystem:       string(v.Target.Ecosystem),
			Package:         v.Target.Package,
			Version:         v.Target.Version,
			Artifact:        v.Target.Artifact,
			Success:         v.Message == "",
			Message:         v.Message,
			Strategy:        v.StrategyOneof,
			Timings:         v.Timings,
			ExecutorVersion: resp.Executor,
			RunID:           sreq.ID,
			Started:         started,
			Created:         time.Now().UTC(),
		})
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "writing record for %s@%s", sreq.Package, v.Target.Version))
		}
	}
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "executing smoketest"))
	}
	return resp, nil
}
