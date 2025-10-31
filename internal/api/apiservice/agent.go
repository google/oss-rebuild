// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"encoding/json"
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
	DefaultMaxIterations = 5
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

// executionFromOp extracts the execution name from the metadata of a
// long-running operation returned by the google.golang.org/api/run/v2 client
// library.
// TODO: Switch to the cloud.google.com/go/run/apiv2 library, which would not
// require us to to do marshalling of operation types to get this data.
// This function returns the full execution resource ID: projects/<project>/locations/<location>/jobs/<job>/executions/<execution>
func executionFromOp(op *run.GoogleLongrunningOperation) (string, error) {
	if op == nil || op.Metadata == nil {
		return "", fmt.Errorf("operation or its metadata is nil")
	}
	metadataBytes, err := op.Metadata.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("failed to marshal operation metadata: %w", err)
	}
	var e run.GoogleCloudRunV2Execution
	if err := json.Unmarshal(metadataBytes, &e); err != nil {
		return "", fmt.Errorf("unmarshalling metadata into GoogleCloudRunV2Execution: %w", err)
	}
	if e.Name == "" {
		return "", fmt.Errorf("execution name is empty")
	}
	return e.Name, nil
}

func AgentCreate(ctx context.Context, req schema.AgentCreateRequest, deps *AgentCreateDeps) (*schema.AgentCreateResponse, error) {
	sessionUUID, err := uuid.NewV7()
	if err != nil {
		return nil, errors.Wrap(err, "making sessionID")
	}
	sessionID := sessionUUID.String()
	sessionTime := time.Unix(sessionUUID.Time().UnixTime())
	// Set defaults for configuration
	maxIterations := req.MaxIterations
	if maxIterations == 0 {
		maxIterations = DefaultMaxIterations
	}
	session := schema.AgentSession{
		ID:             sessionID,
		RunID:          req.RunID,
		Target:         req.Target,
		MaxIterations:  maxIterations,
		TimeoutSeconds: deps.AgentTimeoutSeconds,
		Context:        req.Context,
		Status:         schema.AgentSessionStatusInitializing,
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
	op, err := deps.RunService.Projects.Locations.Jobs.Run(deps.AgentJobName, &run.GoogleCloudRunV2RunJobRequest{
		Overrides: &run.GoogleCloudRunV2Overrides{
			Timeout: fmt.Sprintf("%ds", deps.AgentTimeoutSeconds),
			ContainerOverrides: []*run.GoogleCloudRunV2ContainerOverride{
				{
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
	session.ExecutionName, err = executionFromOp(op)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "getting execution name from operation"))
	}
	session.Status = schema.AgentSessionStatusRunning
	session.Updated = time.Now().UTC()
	_, err = deps.FirestoreClient.Collection("agent_sessions").Doc(sessionID).Set(ctx, session)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating session status"))
	}
	return &schema.AgentCreateResponse{
		SessionID:     sessionID,
		ExeuctionName: session.ExecutionName,
	}, nil
}
