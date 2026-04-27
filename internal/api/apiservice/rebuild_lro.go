// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/run/v2"
)

// RebuildView is a View for rebuild operations.
type RebuildView = longrunning.View[schema.RebuildAttempt, db.AttemptKey, schema.Verdict]

// NewRebuildView returns a new RebuildView for the given attempts resource.
func NewRebuildView(attempts db.Attempts) *RebuildView {
	return &RebuildView{
		Resource:  attempts,
		KeyFor:    toAttemptKey,
		Projector: ProjectRebuildAttempt,
	}
}

// ProjectRebuildAttempt projects a RebuildAttempt into a longrunning.Operation.
func ProjectRebuildAttempt(a schema.RebuildAttempt) longrunning.Operation[schema.Verdict] {
	op := longrunning.Operation[schema.Verdict]{
		ID: toOperationID(db.AttemptKey{Target: a.Target(), RunID: a.RunID}),
		Result: &schema.Verdict{
			Target:        a.Target(),
			Message:       a.Message,
			StrategyOneof: a.Strategy,
			Timings:       a.Timings,
		},
	}
	switch a.Status {
	case schema.RebuildStatusSuccess:
		op.Done = true
	case schema.RebuildStatusFailure, schema.RebuildStatusError:
		op.Done = true
		op.Error = &longrunning.OperationError{
			Code:    http.StatusInternalServerError,
			Message: a.Message,
		}
	case schema.RebuildStatusCancelled:
		op.Done = true
		op.Error = &longrunning.OperationError{
			Code:    http.StatusGone,
			Message: "cancelled",
		}
	default:
		op.Done = false
	}
	return op
}

// toAttemptKey converts an operation ID back into a db.AttemptKey.
func toAttemptKey(id string) (db.AttemptKey, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 5 {
		return db.AttemptKey{}, fmt.Errorf("invalid operation id: %s", id)
	}
	et := rebuild.FirestoreTargetEncoding.New(rebuild.Ecosystem(parts[0]), parts[1], parts[2], parts[3])
	return db.AttemptKey{
		Target: et.Decode(),
		RunID:  parts[4],
	}, nil
}

// toOperationID converts a db.AttemptKey into an operation ID.
func toOperationID(k db.AttemptKey) string {
	et := rebuild.FirestoreTargetEncoding.Encode(k.Target)
	return strings.Join([]string{string(et.Ecosystem), et.Package, et.Version, et.Artifact, k.RunID}, "/")
}

// CreateRebuildOpDeps contains dependencies for creating a rebuild operation.
type CreateRebuildOpDeps struct {
	Attempts   db.Attempts
	RunJob     RunJobFunc
	Project    string
	Location   string
	JobName    string
	DepsConfig schema.RebuildDepsConfig
	DepsFunc   func(context.Context, *schema.RebuildDepsConfig) (*RebuildPackageDeps, error)
}

type RunJobFunc func(ctx context.Context, name string, req *run.GoogleCloudRunV2RunJobRequest) (*run.GoogleLongrunningOperation, error)

// GetRebuildOpDeps contains dependencies for getting a rebuild operation.
type GetRebuildOpDeps struct {
	Reader longrunning.Reader[schema.Verdict]
}

// CreateRebuildOp creates a new rebuild operation.
func CreateRebuildOp(
	ctx context.Context,
	req schema.RebuildPackageRequest,
	deps *CreateRebuildOpDeps,
) (*longrunning.Operation[schema.Verdict], error) {
	key := db.AttemptKey{
		Target: rebuild.Target{
			Ecosystem: req.Ecosystem,
			Package:   req.Package,
			Version:   req.Version,
			Artifact:  req.Artifact,
		},
		RunID: req.ID,
	}
	opID := toOperationID(key)

	switch req.ExecutionHint {
	case schema.ExtendedExecution:
		attempt := schema.RebuildAttempt{
			Ecosystem: string(req.Ecosystem),
			Package:   req.Package,
			Version:   req.Version,
			Artifact:  req.Artifact,
			RunID:     req.ID,
			Status:    schema.RebuildStatusRunning,
		}

		if err := deps.Attempts.Insert(ctx, attempt); err != nil {
			return nil, err
		}

		if err := launchRebuildJob(ctx, deps.RunJob, deps.DepsConfig, opID, req, deps.Project, deps.Location, deps.JobName); err != nil {
			// Best-effort mark the attempt as failed.
			attempt.Status = schema.RebuildStatusError
			attempt.Message = "failed to launch rebuild job: " + err.Error()
			_ = deps.Attempts.Update(ctx, attempt)
			return nil, err
		}

		op := ProjectRebuildAttempt(attempt)
		return &op, nil

	case schema.FastExecution, schema.UnspecifiedExecution:
		rebuildDeps, err := deps.DepsFunc(ctx, &deps.DepsConfig)
		if err != nil {
			return nil, err
		}
		// Execute in-process. RebuildPackage already handles Firestore persistence of the attempt.
		_, _ = RebuildPackage(ctx, req, rebuildDeps)

		// Get the final state from DB to project it correctly.
		attempt, err := deps.Attempts.Get(ctx, key)
		if err != nil {
			return nil, errors.Wrap(err, "fetching finished attempt")
		}
		op := ProjectRebuildAttempt(attempt)
		return &op, nil

	default:
		return nil, errors.Errorf("unhandled execution hint: %s", req.ExecutionHint)
	}
}

// GetRebuildOp gets a rebuild operation.
func GetRebuildOp(
	ctx context.Context,
	req schema.GetOperationRequest,
	deps *GetRebuildOpDeps,
) (*longrunning.Operation[schema.Verdict], error) {
	return deps.Reader.Get(ctx, req.ID)
}

func launchRebuildJob(ctx context.Context, runJob RunJobFunc, cfg schema.RebuildDepsConfig, opID string, req schema.RebuildPackageRequest, project, location, jobName string) error {
	envVars, err := cfg.ToEnv()
	if err != nil {
		return err
	}
	var runEnv []*run.GoogleCloudRunV2EnvVar
	for _, ev := range envVars {
		runEnv = append(runEnv, &run.GoogleCloudRunV2EnvVar{
			Name:  ev.Name,
			Value: ev.Value,
		})
	}
	runEnv = append(runEnv, &run.GoogleCloudRunV2EnvVar{
		Name:  "OP_ID",
		Value: opID,
	})
	// Serialize the request too
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}
	runEnv = append(runEnv, &run.GoogleCloudRunV2EnvVar{
		Name:  "REBUILD_REQUEST",
		Value: string(reqBytes),
	})

	jobFullName := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobName)
	_, err = runJob(ctx, jobFullName, &run.GoogleCloudRunV2RunJobRequest{
		Overrides: &run.GoogleCloudRunV2Overrides{
			ContainerOverrides: []*run.GoogleCloudRunV2ContainerOverride{
				{
					Env: runEnv,
				},
			},
		},
	})
	return err
}
