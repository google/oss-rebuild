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

func RebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	if sreq.ID == "" {
		sreq.ID = time.Now().UTC().Format(time.RFC3339)
	}
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
	resp, stuberr := deps.SmoketestStub(ctx, sreq)
	var verdicts []schema.Verdict
	var executor string
	if errors.Is(stuberr, api.ErrNotOK) {
		log.Printf("smoketest failed: %v\n", stuberr)
		// If smoketest failed, populate the verdicts with as much info as we can (pulling executor
		// version from the smoketest version endpoint if possible.
		executor = <-versionChan
		for _, v := range sreq.Versions {
			verdicts = append(verdicts, schema.Verdict{
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
	} else if stuberr != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(stuberr, "making smoketest request"))
	} else {
		verdicts = resp.Verdicts
		executor = resp.Executor
	}
	for _, v := range verdicts {
		_, err := deps.FirestoreClient.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(sreq.Package)).Collection("versions").Doc(v.Target.Version).Collection("attempts").Doc(sreq.ID).Set(ctx, schema.SmoketestAttempt{
			Ecosystem:       string(v.Target.Ecosystem),
			Package:         v.Target.Package,
			Version:         v.Target.Version,
			Artifact:        v.Target.Artifact,
			Success:         v.Message == "",
			Message:         v.Message,
			Strategy:        v.StrategyOneof,
			Timings:         v.Timings,
			ExecutorVersion: executor,
			RunID:           sreq.ID,
			Created:         time.Now().UnixMilli(),
		})
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "writing record for %s@%s", sreq.Package, v.Target.Version))
		}
	}
	if stuberr != nil {
		// TODO: Pass on status code here.
		return nil, api.AsStatus(codes.Internal, errors.Wrap(stuberr, "executing smoketest"))
	}
	return resp, nil
}
