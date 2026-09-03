// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type AgentCompleteDeps struct {
	Sessions db.Sessions
	// Scratches and GCE enable best-effort teardown of a scratch-mode
	// session's VM at completion. Optional: when either is nil the scratch
	// is left to the idle reaper.
	Scratches db.Scratch
	GCE       GCE
}

func AgentComplete(ctx context.Context, req schema.AgentCompleteRequest, deps *AgentCompleteDeps) (*schema.AgentCompleteResponse, error) {
	if req.SessionID == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("session_id required"))
	}
	var session schema.AgentSession
	err := deps.Sessions.Mutate(ctx, req.SessionID, func(s *schema.AgentSession) (bool, error) {
		defer func() { session = *s }()
		if s.Status == schema.AgentSessionStatusCompleted {
			return false, nil // Already completed, no-op
		}
		s.Status = schema.AgentSessionStatusCompleted
		s.StopReason = req.StopReason
		s.Updated = time.Now().UTC()
		if req.SuccessIterationID != "" {
			s.SuccessIteration = req.SuccessIterationID
		}
		if req.Summary != "" {
			s.Summary = req.Summary
		}
		if req.Usage != nil {
			s.Usage = req.Usage // token consumption, as tallied by the agent
		}
		return true, nil
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating session completion"))
	}
	// Eagerly release the session's scratch VM, if any. Best effort: failures
	// are logged and left to the idle reaper.
	if session.ScratchID != "" && deps.Scratches != nil && deps.GCE != nil {
		if scratch, err := deps.Scratches.Get(ctx, session.ScratchID); err != nil {
			log.Printf("session %s: fetching scratch %s for teardown: %v", req.SessionID, session.ScratchID, err)
		} else if scratch.State != schema.ScratchDeleting && scratch.State != schema.ScratchDeleted {
			if _, err := ScratchDelete(ctx, schema.ScratchDeleteRequest{ScratchID: session.ScratchID}, &ScratchDeleteDeps{
				Scratches: deps.Scratches,
				GCE:       deps.GCE,
			}); err != nil {
				log.Printf("session %s: releasing scratch %s: %v", req.SessionID, session.ScratchID, err)
			}
		}
	}
	return &schema.AgentCompleteResponse{Success: true}, nil
}
