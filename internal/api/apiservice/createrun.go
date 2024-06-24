package apiservice

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type CreateRunDeps struct {
	FirestoreClient *firestore.Client
}

func CreateRun(ctx context.Context, req schema.CreateRunRequest, deps *CreateRunDeps) (*schema.CreateRunResponse, error) {
	id := time.Now().UTC().Format(time.RFC3339)
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		return t.Create(deps.FirestoreClient.Collection("runs").Doc(id), map[string]any{
			"benchmark_name": req.Name,
			"benchmark_hash": req.Hash,
			"run_type":       req.Type,
			"created":        time.Now().UTC().UnixMilli(),
		})
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "firestore write"))
	}
	return &schema.CreateRunResponse{ID: id}, nil
}
