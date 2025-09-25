// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultMaxIterations is the default maximum number of iterations for an agent session
	DefaultMaxIterations = 20
)

type AgentCreateDeps struct {
	FirestoreClient     *firestore.Client
	RunService          *run.Service
	Project             string
	Location            string
	AgentJobName        string
	AgentAPIURL         string
	AgentTimeoutSeconds int
	SessionsBucket      string
	MetadataBucket      string
	LogsBucket          string
}

func AgentCreate(ctx context.Context, req schema.AgentCreateRequest, deps *AgentCreateDeps) (*schema.AgentCreateResponse, error) {
	sessionUUID, err := uuid.NewV7()
	if err != nil {
		return nil, errors.Wrap(err, "making sessionID")
	}
	sessionID := sessionUUID.String()
	sessionTime := time.Unix(sessionUUID.Time().UnixTime())
	jobName := fmt.Sprintf("agent-%s", sessionID)
	// Set defaults for configuration
	maxIterations := req.MaxIterations
	if maxIterations == 0 {
		maxIterations = DefaultMaxIterations
	}
	session := schema.AgentSession{
		ID:             sessionID,
		Target:         req.Target,
		MaxIterations:  maxIterations,
		TimeoutSeconds: deps.AgentTimeoutSeconds,
		Context:        req.Context,
		Status:         schema.AgentSessionStatusInitializing,
		JobName:        jobName,
		Created:        sessionTime,
		Updated:        sessionTime,
	}
	// Create session in Firestore
	err = deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		// NOTE: This would fail if the session already exists.
		return t.Create(deps.FirestoreClient.Collection("agent_sessions").Doc(sessionID), session)
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return nil, api.AsStatus(codes.AlreadyExists, errors.Errorf("agent session %s already exists", sessionID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating agent session"))
	}
	// Create Cloud Run Job
	_, err = deps.RunService.Projects.Locations.Jobs.Run(deps.AgentJobName, &run.GoogleCloudRunV2RunJobRequest{
		Overrides: &run.GoogleCloudRunV2Overrides{
			Timeout: fmt.Sprintf("%ds", deps.AgentTimeoutSeconds),
			ContainerOverrides: []*run.GoogleCloudRunV2ContainerOverride{
				{
					Name: jobName,
					Args: []string{
						"--project=" + deps.Project,
						"--location=" + deps.Location,
						"--session-id=" + sessionID,
						"--agent-api-url=" + deps.AgentAPIURL,
						"--sessions-bucket=" + deps.SessionsBucket,
						"--metadata-bucket=" + deps.MetadataBucket,
						"--logs-bucket=" + deps.LogsBucket,
						"--max-iterations=" + fmt.Sprintf("%d", maxIterations),
						"--target-ecosystem=" + string(req.Target.Ecosystem),
						"--target-package=" + req.Target.Package,
						"--target-version=" + req.Target.Version,
						"--target-artifact=" + req.Target.Artifact,
					}},
			},
		},
	}).Do()
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating cloud run job"))
	}
	// Update session status
	session.Status = schema.AgentSessionStatusRunning
	session.Updated = time.Now().UTC()
	_, err = deps.FirestoreClient.Collection("agent_sessions").Doc(sessionID).Set(ctx, session)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating session status"))
	}
	return &schema.AgentCreateResponse{
		SessionID: sessionID,
		JobName:   jobName,
	}, nil
}
