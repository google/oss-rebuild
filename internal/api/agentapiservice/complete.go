// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type AgentCompleteDeps struct {
	FirestoreClient *firestore.Client
}

func AgentComplete(ctx context.Context, req schema.AgentCompleteRequest, deps *AgentCompleteDeps) (*schema.AgentCompleteResponse, error) {
	if req.SessionID == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("session_id required"))
	}
	// Fetch and update session in a transaction
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		sessionDoc := deps.FirestoreClient.Collection("agent_sessions").Doc(req.SessionID)
		docSnap, err := t.Get(sessionDoc)
		if err != nil {
			return errors.Wrap(err, "fetching session")
		}
		var session schema.AgentSession
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
	return &schema.AgentCompleteResponse{Success: true}, nil
}
