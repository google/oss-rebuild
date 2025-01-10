package feed

import (
	"context"
	"net/url"

	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type RebuildFeeder interface {
	Add(context.Context, []rebuild.Target) error
}

type feeder struct {
	apiURL  *url.URL
	queue   taskqueue.Queue
	tracker Tracker
	runID   string
}

var _ RebuildFeeder = &feeder{}

func NewRebuildFeeder(ctx context.Context, taskQueuePath, serviceAccountEmail string, apiURL *url.URL, runID string, tracker Tracker) (RebuildFeeder, error) {
	queue, err := taskqueue.NewQueue(ctx, taskQueuePath, serviceAccountEmail)
	if err != nil {
		return nil, errors.Wrap(err, "creating task queue")
	}
	return &feeder{
		apiURL:  apiURL,
		queue:   queue,
		tracker: tracker,
		runID:   runID,
	}, nil
}

func (rbf *feeder) Add(ctx context.Context, targets []rebuild.Target) error {
	// TODO: Add support for schema.ExecutionMode (only supports schema.Attest currently).
	for _, t := range targets {
		tracked, err := rbf.tracker.IsTracked(t)
		if err != nil {
			return errors.Wrap(err, "checking if target is tracked")
		}
		if !tracked {
			continue
		}
		req := schema.RebuildPackageRequest{
			Ecosystem: t.Ecosystem,
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
			// Creating a new run object for each execution would be bloat (thousands per day).
			// If people want to query recent executions they can query using the static ID and flter based on the RebuildAttempt.Created field.
			ID: rbf.runID,
		}
		if _, err := rbf.queue.Add(ctx, rbf.apiURL.JoinPath("rebuild").String(), req); err != nil {
			return errors.Wrap(err, "queing rebuild task")
		}
	}
	return nil
}
