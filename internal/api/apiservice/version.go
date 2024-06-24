package apiservice

import (
	"context"
	"errors"
	"os"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
)

type VersionDeps struct {
	FirestoreClient       *firestore.Client
	BuildLocalVersionStub api.StubT[schema.VersionRequest, schema.VersionResponse]
	InferenceVersionStub  api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func Version(ctx context.Context, req schema.VersionRequest, deps *VersionDeps) (*schema.VersionResponse, error) {
	switch req.Service {
	case "":
		return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
	case "build-local":
		return deps.BuildLocalVersionStub(ctx, req)
	case "inference":
		return deps.InferenceVersionStub(ctx, req)
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unknown service"))
	}
}
