// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
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
	ScratchCreateStub   api.StubFn[schema.ScratchCreateRequest, schema.Scratch]               // Allocates a scratch VM for scratch-mode sessions. nil disables scratch execution mode.
	ScratchDeleteStub   api.StubFn[schema.ScratchDeleteRequest, schema.ScratchDeleteResponse] // Releases the scratch when session setup fails after allocation. Optional: cleanup is skipped when nil.
	PrebuildConfig      rebuild.PrebuildConfig                                                // Locates prebuilt build tools (timewarp) which scratch-mode agents fetch into their build containers.
	Host                string                                                                // Forwarded to agents to identify their outbound registry traffic (see httpegress.Config.Host).
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
	executionMode := req.ExecutionMode
	if executionMode == "" {
		executionMode = schema.AgentExecutionModeGCB
	}
	if executionMode == schema.AgentExecutionModeScratch && deps.ScratchCreateStub == nil {
		return nil, api.AsStatus(codes.FailedPrecondition, errors.New("scratch execution mode is not configured"))
	}
	taskMode := req.TaskMode
	if taskMode == "" {
		taskMode = schema.AgentTaskModeDebug
	}
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
		ExecutionMode:  executionMode,
		TaskMode:       taskMode,
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
	if executionMode == schema.AgentExecutionModeScratch {
		// Allocate the session's scratch VM. The agent is deliberately not
		// responsible for allocation: it only receives the handle to exec
		// against. AgentComplete (or the idle reaper as backstop) tears it down.
		scratch, err := deps.ScratchCreateStub(ctx, schema.ScratchCreateRequest{
			BuildID:      sessionID,
			MachineClass: schema.MachineClassStandard,
		})
		if err != nil {
			// The session doc already exists. Without a terminal update it
			// would sit INITIALIZING forever (no execution will complete it).
			session.Status = schema.AgentSessionStatusCompleted
			session.StopReason = schema.AgentCompleteReasonError
			session.Summary = fmt.Sprintf("Allocating scratch: %v", err)
			session.Updated = time.Now().UTC()
			if _, serr := deps.FirestoreClient.Collection("agent_sessions").Doc(sessionID).Set(ctx, session); serr != nil {
				log.Printf("terminating session %s after scratch allocation failure: %v", sessionID, serr)
			}
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "allocating scratch"))
		}
		session.ScratchID = scratch.ID
	}
	// releaseScratch cleans up the allocated scratch when session setup fails
	// after allocation. Best effort: the idle reaper is the backstop.
	releaseScratch := func() {
		if session.ScratchID == "" || deps.ScratchDeleteStub == nil {
			return
		}
		if _, err := deps.ScratchDeleteStub(ctx, schema.ScratchDeleteRequest{ScratchID: session.ScratchID}); err != nil {
			log.Printf("releasing scratch %s for failed session %s: %v", session.ScratchID, sessionID, err)
		}
	}
	if !req.ExternalAgent {
		args := []string{
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
		}
		if deps.Host != "" {
			args = append(args, "--host="+deps.Host)
		}
		if executionMode == schema.AgentExecutionModeScratch {
			args = append(args,
				"--execution-mode="+string(schema.AgentExecutionModeScratch),
				"--scratch-id="+session.ScratchID,
				"--prebuild-bucket="+deps.PrebuildConfig.Bucket,
				"--prebuild-dir="+deps.PrebuildConfig.Dir,
				fmt.Sprintf("--prebuild-auth=%t", deps.PrebuildConfig.Auth),
			)
		}
		// Create Cloud Run Job
		op, err := deps.RunService.Projects.Locations.Jobs.Run(deps.AgentJobName, &run.GoogleCloudRunV2RunJobRequest{
			Overrides: &run.GoogleCloudRunV2Overrides{
				Timeout: fmt.Sprintf("%ds", deps.AgentTimeoutSeconds),
				ContainerOverrides: []*run.GoogleCloudRunV2ContainerOverride{
					{Args: args},
				},
			},
		}).Do()
		if err != nil {
			releaseScratch()
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating cloud run job"))
		}
		session.ExecutionName, err = executionFromOp(op)
		if err != nil {
			// NOTE: Failing here would strand the launched job (a retry
			// launches a second job and allocates a second scratch) and skip
			// persisting ScratchID, which AgentComplete's teardown reads.
			// Proceed without an execution name, as external-agent sessions do.
			log.Printf("session %s: getting execution name from operation: %v", sessionID, err)
		}
	}
	// Update session status. External-agent sessions become RUNNING without an
	// execution: the caller runs the agent binary externally.
	session.Status = schema.AgentSessionStatusRunning
	session.Updated = time.Now().UTC()
	_, err = deps.FirestoreClient.Collection("agent_sessions").Doc(sessionID).Set(ctx, session)
	if err != nil {
		if req.ExternalAgent {
			// No agent will ever use the scratch. Release it eagerly.
			releaseScratch()
		}
		// NOTE: With a launched job the scratch is left for the (possibly
		// still viable) execution. The reaper handles any orphan.
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "updating session status"))
	}
	return &schema.AgentCreateResponse{
		SessionID:     sessionID,
		ExeuctionName: session.ExecutionName,
		ScratchID:     session.ScratchID,
	}, nil
}
