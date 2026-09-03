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
	Sessions  db.Sessions
	Scratches db.Scratch  // with Hosts, tears down guests
	Hosts     *HostClient // nil leaves teardown to the reaper
	GCE       GCE         // per-VM-era records. May be nil
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
	// Release the guest now. The reaper is the backstop.
	if session.ScratchID != "" && deps.Scratches != nil && deps.Hosts != nil {
		if scratch, err := deps.Scratches.Get(ctx, session.ScratchID); err != nil {
			log.Printf("session %s: fetching scratch %s for teardown: %v", req.SessionID, session.ScratchID, err)
		} else if scratch.State != schema.ScratchDeleting && scratch.State != schema.ScratchDeleted {
			if err := deleteScratch(ctx, deps.Scratches, deps.GCE, deps.Hosts, scratch, nil); err != nil {
				log.Printf("session %s: releasing scratch %s: %v", req.SessionID, session.ScratchID, err)
			}
		}
	}
	return &schema.AgentCompleteResponse{Success: true}, nil
}
