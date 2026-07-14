// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type AgentCompleteDeps struct {
	FirestoreClient *firestore.Client
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
	// Fetch and update session in a transaction
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		sessionDoc := deps.FirestoreClient.Collection("agent_sessions").Doc(req.SessionID)
		docSnap, err := t.Get(sessionDoc)
		if err != nil {
			return errors.Wrap(err, "fetching session")
		}
		if err := docSnap.DataTo(&session); err != nil {
			return errors.Wrap(err, "parsing session data")
		}
		// Check if already completed
		if session.Status == schema.AgentSessionStatusCompleted {
			return nil // Already completed, no-op
		}
		// Update session with completion details
		session.Status = schema.AgentSessionStatusCompleted
		session.StopReason = req.StopReason
		session.Updated = time.Now().UTC()
		if req.SuccessIterationID != "" {
			session.SuccessIteration = req.SuccessIterationID
		}
		if req.Summary != "" {
			session.Summary = req.Summary
		}
		return t.Set(sessionDoc, session)
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
