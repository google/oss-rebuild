// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"io/fs"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AgentCreateIterationDeps struct {
	FirestoreClient     *firestore.Client
	GCBExecutor         *gcb.Executor
	BuildProject        string
	BuildServiceAccount string
	LogsBucket          string
	MetadataBucket      string
	PrebuildConfig      rebuild.PrebuildConfig
}

func AgentCreateIteration(ctx context.Context, req schema.AgentCreateIterationRequest, deps *AgentCreateIterationDeps) (*schema.AgentCreateIterationResponse, error) {
	if req.SessionID == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("session_id required"))
	}
	obliviousID := uuid.New().String()
	iterTime := time.Now().UTC()
	iterationID := iterTime.Format(time.RFC3339)
	var iteration schema.AgentIteration
	var session schema.AgentSession
	// Create iteration record and fetch session in a transaction
	sessionDoc := deps.FirestoreClient.Collection("agent_sessions").Doc(req.SessionID)
	iterDoc := sessionDoc.Collection("agent_iterations").Doc(iterationID)
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		// Fetch session to get Target and validate it exists
		_, err := t.Get(sessionDoc)
		if err != nil {
			return errors.Wrap(err, "fetching session")
		}
		// Get the highest iteration number for this session to increment it
		iterQuery := sessionDoc.Collection("agent_iterations").
			Where("session_id", "==", req.SessionID).
			Where("number", "==", req.IterationNumber).
			Limit(1)
		if _, err := t.Documents(iterQuery).Next(); err != nil && status.Code(err) != codes.NotFound {
			return errors.Wrap(err, "checking for existing iteration")
		} else if err == nil {
			return errors.Wrap(fs.ErrExist, "checking for existing iteration")
		}
		// Create iteration record
		iteration = schema.AgentIteration{
			ID:          iterationID,
			SessionID:   req.SessionID,
			Number:      req.IterationNumber,
			Strategy:    req.Strategy,
			ObliviousID: obliviousID,
			Status:      schema.AgentIterationStatusPending,
			Created:     iterTime,
			Updated:     iterTime,
		}
		return t.Create(iterDoc, iteration)
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, api.AsStatus(codes.NotFound, errors.New("session not found"))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating iteration record"))
	}
	// Extract strategy and use GCB executor to plan and execute build
	if req.Strategy == nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("strategy is required"))
	}
	strategy, err := req.Strategy.Strategy()
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "invalid strategy"))
	}
	// Use GCB executor to plan and execute the build using Target from session
	store, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, obliviousID), "gs://"+deps.MetadataBucket)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating GCS store"))
	}
	// Build tool URLs using prebuild bucket configuration
	toolURLs := map[build.ToolType]string{
		build.TimewarpTool: "gs://" + deps.PrebuildConfig.Bucket + "/" + deps.PrebuildConfig.Dir + "/timewarp",
		build.GSUtilTool:   "gs://" + deps.PrebuildConfig.Bucket + "/" + deps.PrebuildConfig.Dir + "/gsutil-writeonly",
	}
	var authRequired []string
	if deps.PrebuildConfig.Auth {
		authRequired = append(authRequired, "gs://"+deps.PrebuildConfig.Bucket)
	}
	h, err := deps.GCBExecutor.Start(ctx, rebuild.Input{
		Target:   session.Target,
		Strategy: strategy,
	}, build.Options{
		BuildID:     obliviousID,
		UseTimewarp: true,
		Resources: build.Resources{
			AssetStore:       store,
			ToolURLs:         toolURLs,
			ToolAuthRequired: authRequired,
			BaseImageConfig:  build.DefaultBaseImageConfig(),
		},
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "starting build"))
	}
	// Update iteration with build details
	iteration.Status = schema.AgentIterationStatusBuilding
	iteration.Updated = time.Now().UTC()
	_, err = iterDoc.Set(ctx, iteration)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating iteration status"))
	}
	// NOTE: For now, we block and wait for the build to complete
	result, err := h.Wait(ctx)
	// Update iteration with result
	iteration.Updated = time.Now().UTC()
	if err != nil {
		iteration.Status = schema.AgentIterationStatusError
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: false,
			ErrorMessage: err.Error(),
		}
	} else if result.Error != nil {
		iteration.Status = schema.AgentIterationStatusFailed
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: false,
			ErrorMessage: result.Error.Error(),
		}
	} else {
		iteration.Status = schema.AgentIterationStatusSuccess
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: true,
			ErrorMessage: "",
		}
	}
	_, err = iterDoc.Set(ctx, iteration)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating iteration with result"))
	}
	return &schema.AgentCreateIterationResponse{
		IterationID: iterationID,
		ObliviousID: obliviousID,
	}, nil
}
