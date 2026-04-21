// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"syscall"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var poolPat = regexp.MustCompile(`^projects/(?P<project>[^/]+)/locations/(?P<location>[^/]+)/workerPools/(?P<pool>[^/]+)$`)

// PrivatePoolConfig holds configuration for using GCB private pools.
type PrivatePoolConfig struct {
	// Resource name of the private pool (e.g., "projects/PROJECT_ID/locations/LOCATION/workerPools/POOL_NAME")
	Name string
	// Region where the private pool builds should be run (e.g., "us-central1")
	Region string
}

// Client interface abstracts Cloud Build service interactions.
type Client interface {
	CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	WaitForOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
	CancelOperation(op *cloudbuild.Operation) error
}

// LogsClient interface abstracts Cloud Build logs access.
type LogsClient interface {
	// ReadBuildLogs reads the complete build logs for a given build ID
	ReadBuildLogs(ctx context.Context, buildID string) (io.ReadCloser, error)
	// ReadStepLogs reads logs for a specific step within a build
	ReadStepLogs(ctx context.Context, buildID string, stepIndex int) (io.ReadCloser, error)
	// ListStepLogs returns the available step log files for a build
	ListStepLogs(ctx context.Context, buildID string) (int, error)
}

// gcsLogsClient implements LogsClient using Google Cloud Storage.
type gcsLogsClient struct {
	gcsClient *gcs.Client
	bucket    *gcs.BucketHandle
}

// NewGCSLogsClient creates a new LogsClient that reads from Google Cloud Storage.
func NewGCSLogsClient(gcsClient *gcs.Client, bucket string) LogsClient {
	return &gcsLogsClient{
		gcsClient: gcsClient,
		bucket:    gcsClient.Bucket(bucket),
	}
}

// ReadBuildLogs reads the complete build logs for a given build ID from the specified bucket.
func (c *gcsLogsClient) ReadBuildLogs(ctx context.Context, buildID string) (io.ReadCloser, error) {
	objectName := fmt.Sprintf("log-%s.txt", buildID)
	return c.bucket.Object(objectName).NewReader(ctx)
}

// ReadStepLogs reads logs for a specific step within a build from the specified bucket.
func (c *gcsLogsClient) ReadStepLogs(ctx context.Context, buildID string, stepIndex int) (io.ReadCloser, error) {
	objectName := fmt.Sprintf("log-%s.%d.txt", buildID, stepIndex)
	return c.bucket.Object(objectName).NewReader(ctx)
}

// ListStepLogs returns the available step log files for a build in the specified bucket.
func (c *gcsLogsClient) ListStepLogs(ctx context.Context, buildID string) (int, error) {
	maxSteps := 100
	for i := range maxSteps {
		objectName := fmt.Sprintf("log-%s.%d.txt", buildID, i)
		_, err := c.bucket.Object(objectName).Attrs(ctx)
		if errors.Is(err, gcs.ErrObjectNotExist) {
			return i - 1, nil
		} else if err != nil {
			return 0, err
		}
	}
	return maxSteps, nil
}

// clientImpl is a concrete implementation of the Client interface using the Cloud Build service.
type clientImpl struct {
	service      *cloudbuild.Service
	pollInterval time.Duration
	region       string
}

// NewClient creates a new Client with the given options.
func NewClient(s *cloudbuild.Service) Client {
	// TODO: Add optional configuration of poll value if/when needed.
	return &clientImpl{
		service:      s,
		pollInterval: 10 * time.Second, // default GCB API quota is low
	}
}

// NewRegionalClient creates a new Client that uses regional GCB backends.
func NewRegionalClient(s *cloudbuild.Service, region string) Client {
	return &clientImpl{
		service:      s,
		pollInterval: 10 * time.Second, // default GCB API quota is low
		region:       region,
	}
}

// CreateBuild creates and starts a GCB Build.
func (c *clientImpl) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	if build.Options != nil && build.Options.Pool != nil {
		if c.region == "" {
			return nil, fmt.Errorf("attempting to use a non-regional client with private pool: %s", build.Options.Pool.Name)
		}
		// For private pools, use the regional API endpoint if specified
		// TODO: Should we verify these regions match?
		if matches := poolPat.FindStringSubmatch(build.Options.Pool.Name); matches != nil {
			p := matches[poolPat.SubexpIndex("project")] // ignore provided project, extract from the pool
			l := matches[poolPat.SubexpIndex("location")]
			if l != c.region || p != project {
				return nil, fmt.Errorf("build pool '%s' doesn't match region: %s or project: %s", build.Options.Pool.Name, c.region, project)
			}
			return c.service.Projects.Locations.Builds.Create(fmt.Sprintf("projects/%s/locations/%s", p, l), build).Context(ctx).Do()
		} else {
			return nil, fmt.Errorf("invalid pool name: '%s'", build.Options.Pool.Name)
		}
	}
	return c.service.Projects.Builds.Create(project, build).Context(ctx).Do()
}

// WaitForOperation polls and waits for the operation to complete.
// Operations.Get() will respect context deadlines, in which case that error will be returned
func (c *clientImpl) WaitForOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	for !op.Done {
		select {
		// Wait for ctx.Done() in case a cancel is called during the pollInterval.
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.pollInterval):
			var err error
			op, err = c.operations().Get(op.Name).Context(ctx).Do()
			if errors.Is(err, syscall.ECONNRESET) { // Retry-able error
				continue
			}
			if err != nil {
				return nil, errors.Wrap(err, "fetching operation")
			}
		}
	}
	return op, nil
}

func (c *clientImpl) operations() *cloudbuild.OperationsService {
	if c.region != "" {
		// NOTE: There is currently no resource name routing to regional backends due to GCB's legacy operation ID format.
		// This workaround encodes the proper regional backend in the domain so we query the right db.
		regionalService := *c.service
		regionalService.BasePath = fmt.Sprintf("https://%s-cloudbuild.googleapis.com", c.region)
		return cloudbuild.NewOperationsService(&regionalService)
	} else {
		return c.service.Operations
	}
}

func (c *clientImpl) CancelOperation(op *cloudbuild.Operation) error {
	_, err := c.operations().Cancel(op.Name, &cloudbuild.CancelOperationRequest{}).Do()
	return err
}

type DoBuildOpts struct {
	TerminateOnTimeout bool
}

// DoBuild executes a build on Cloud Build, waits for completion and returns the Build.
func DoBuild(ctx context.Context, client Client, project string, build *cloudbuild.Build, opts DoBuildOpts) (*cloudbuild.Build, error) {
	initOp, err := client.CreateBuild(ctx, project, build)
	if err != nil {
		return nil, err
	}
	var bm cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal(initOp.Metadata, &bm); err != nil {
		return nil, err
	}
	doneOp, err := client.WaitForOperation(ctx, initOp)
	if errors.Is(err, context.DeadlineExceeded) && opts.TerminateOnTimeout {
		log.Printf("GCB deadline exceeded, cancelling build %s", bm.Build.Id)
		if err := client.CancelOperation(initOp); err != nil {
			log.Printf("Best effort GCB cancellation failed: %v", err)
			return nil, errors.Wrap(err, "cancelling operation")
		}
		// We can wait 10 more seconds for operation to be updated
		newCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Wait for the now-cancelled op, then proceed normally.
		if doneOp, err = client.WaitForOperation(newCtx, initOp); err != nil {
			return nil, errors.Wrap(err, "fetching operation after cancel")
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("Deadline exceeded waiting for GCB, allowing build %s to continue", bm.Build.Id)
		// NOTE: This is the Build metadata returned by CreateBuild
		return bm.Build, err
	} else if err != nil {
		// NOTE: We could potentially also cancel these unknown error cases, not just DeadlineExceeded
		return nil, errors.Wrap(err, "waiting for operation")
	}
	// NOTE: Build status check will handle failures with better error messages.
	if doneOp.Error != nil {
		log.Printf("Cloud Build error: %v", status.Error(codes.Code(doneOp.Error.Code), doneOp.Error.Message))
	}
	if err := json.Unmarshal(doneOp.Metadata, &bm); err != nil {
		return nil, err
	}
	return bm.Build, nil
}

func ToError(build *cloudbuild.Build) error {
	switch build.Status {
	case "SUCCESS":
		return nil
	case "FAILURE":
		return errors.Errorf("GCB build failed: %s", build.StatusDetail)
	case "TIMEOUT":
		return errors.Errorf("GCB build timeout: %s", build.StatusDetail)
	case "CANCELLED":
		return errors.Errorf("GCB build cancelled: %s", build.StatusDetail)
	case "INTERNAL_ERROR", "EXPIRED":
		return errors.Errorf("GCB build internal error: %s", build.StatusDetail)
	default:
		return errors.Errorf("Unexpected build status: %s", build.Status)
	}

}

// NOTE: There are also per-step logs available as log-<id>-step-<n>.txt
func MergedLogFile(buildID string) string {
	return fmt.Sprintf("log-%s.txt", buildID)
}
