// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"bytes"
	"context"
	"crypto"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type AgentCreateIterationDeps struct {
	Sessions            db.Sessions
	Iterations          db.Iterations
	GCBExecutor         *gcb.Executor
	BuildProject        string
	BuildServiceAccount string
	MetadataBucket      string
	PrebuildConfig      rebuild.PrebuildConfig
	Host                string
}

// Session states that refuse a new iteration.
var (
	errSessionNotRunning = errors.New("session is not running")
	errIterationLimit    = errors.New("session reached its iteration limit")
)

// reserveIteration claims the session's next iteration number: the session
// must be RUNNING and below MaxIterations. The returned session carries the
// claimed number as IterationCount. The agent is not trusted to number or
// bound its own iterations.
func reserveIteration(ctx context.Context, sessions db.Sessions, sessionID string, now time.Time) (schema.AgentSession, error) {
	var session schema.AgentSession
	err := sessions.Mutate(ctx, sessionID, func(s *schema.AgentSession) (bool, error) {
		if s.Status != schema.AgentSessionStatusRunning {
			return false, errSessionNotRunning
		}
		if s.IterationCount >= s.MaxIterations {
			return false, errIterationLimit
		}
		s.IterationCount++
		s.Updated = now
		session = *s
		return true, nil
	})
	switch {
	case err == nil:
		return session, nil
	case errors.Is(err, db.ErrNotFound):
		return session, api.AsStatus(codes.NotFound, errors.New("session not found"))
	case errors.Is(err, errSessionNotRunning), errors.Is(err, errIterationLimit):
		return session, api.AsStatus(codes.FailedPrecondition, errors.Wrap(err, sessionID))
	default:
		return session, api.AsStatus(codes.Internal, errors.Wrap(err, "reserving iteration"))
	}
}

func AgentCreateIteration(ctx context.Context, req schema.AgentCreateIterationRequest, deps *AgentCreateIterationDeps) (*schema.AgentCreateIterationResponse, error) {
	if req.SessionID == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("session_id required"))
	}
	if req.Strategy == nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("strategy is required"))
	}
	strategy, err := req.Strategy.Strategy()
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "invalid strategy"))
	}
	obliviousID := uuid.New().String()
	iterTime := time.Now().UTC()
	iterationID := iterTime.Format(time.RFC3339Nano)
	session, err := reserveIteration(ctx, deps.Sessions, req.SessionID, iterTime)
	if err != nil {
		return nil, err
	}
	iteration := schema.AgentIteration{
		ID:          iterationID,
		SessionID:   req.SessionID,
		Number:      session.IterationCount,
		Strategy:    req.Strategy,
		ObliviousID: obliviousID,
		Status:      schema.AgentIterationStatusPending,
		Usage:       req.Usage, // preserved across the status updates below
		Created:     iterTime,
		Updated:     iterTime,
	}
	// A failed insert leaves the reserved number unused.
	if err := deps.Iterations.Insert(ctx, iteration); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating iteration record"))
	}
	// Use GCB executor to plan and execute the build using Target from session
	store, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, obliviousID), "gs://"+deps.MetadataBucket)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating GCS store"))
	}
	// Build tool URLs using prebuild bucket configuration
	toolURLs := map[build.ToolType]string{
		build.TimewarpTool: "gs://" + deps.PrebuildConfig.Bucket + "/" + deps.PrebuildConfig.Dir + "/timewarp",
		build.GSUtilTool:   "gs://" + deps.PrebuildConfig.Bucket + "/" + deps.PrebuildConfig.Dir + "/gsutil_writeonly",
	}
	var authRequired []string
	if deps.PrebuildConfig.Auth {
		authRequired = append(authRequired, "gs://"+deps.PrebuildConfig.Bucket)
	}
	input := rebuild.Input{
		Target:   session.Target,
		Strategy: strategy,
	}
	h, err := deps.GCBExecutor.Start(ctx, input, build.Options{
		BuildID:            obliviousID,
		UseTimewarp:        meta.AllRebuilders[input.Target.Ecosystem].UsesTimewarp(input),
		SaveContainerImage: true,
		// TODO: Should we set a Timeout?
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
	err = deps.Iterations.Update(ctx, iteration)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating iteration status"))
	}
	// NOTE: For now, we block and wait for the build to complete
	result, buildErr := h.Wait(ctx)

	var exactMatch, stabilizedMatch bool
	if buildErr == nil && result.Error == nil {
		hashes := []crypto.Hash{crypto.SHA256}
		stabilizers, err := stability.StabilizersForTarget(session.Target)
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "getting stabilizers for target"))
		}

		rebuilder, ok := meta.AllRebuilders[session.Target.Ecosystem]
		if !ok {
			return nil, api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
		}
		regclient, err := httpegress.MakeClient(ctx, httpegress.Config{Host: deps.Host})
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.New("making gateway client"))
		}
		mux := meta.NewRegistryMux(regclient)
		upstreamURI, err := rebuilder.UpstreamURL(ctx, session.Target, mux)
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "getting upstream url"))
		}

		rb, up, err := verifier.SummarizeArtifacts(ctx, store, session.Target, upstreamURI, hashes, stabilizers)
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "summarizing artifacts"))
		}
		exactMatch = bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
		stabilizedMatch = bytes.Equal(rb.StabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil))
	}

	// Update iteration with result
	iteration.Updated = time.Now().UTC()
	if buildErr != nil {
		iteration.Status = schema.AgentIterationStatusError
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: false,
			ErrorMessage: buildErr.Error(),
		}
	} else if result.Error != nil {
		iteration.Status = schema.AgentIterationStatusFailed
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: false,
			ErrorMessage: result.Error.Error(),
		}
	} else if !exactMatch && !stabilizedMatch {
		iteration.Status = schema.AgentIterationStatusFailed
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: false,
			ErrorMessage: "rebuild content mismatch",
		}
	} else {
		iteration.Status = schema.AgentIterationStatusSuccess
		iteration.Result = &schema.AgentBuildResult{
			BuildSuccess: true,
			ErrorMessage: "",
		}
	}
	if err := deps.Iterations.Update(ctx, iteration); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating iteration with result"))
	}
	return &schema.AgentCreateIterationResponse{
		IterationID: iterationID,
		ObliviousID: obliviousID,
		Iteration:   &iteration,
	}, nil
}
