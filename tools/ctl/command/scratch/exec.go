// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type execConfig struct {
	API            string
	ScratchID      string
	Cwd            string
	TimeoutSeconds int
	WaitSeconds    int
	PollInterval   time.Duration
	Cmd            []string
}

func (c execConfig) Validate() error {
	if c.API == "" {
		return errors.New("--api is required")
	}
	if c.ScratchID == "" {
		return errors.New("--scratch-id is required")
	}
	if len(c.Cmd) == 0 {
		return errors.New("a command is required (pass it after --, e.g. -- ls -la)")
	}
	if c.PollInterval <= 0 {
		return errors.New("--poll-interval must be positive")
	}
	return nil
}

// parseExecArgs captures the positional command (everything after --) into
// the config's Cmd, run as argv on the worker (no shell). Use
// `-- bash -c "..."` for shell features.
func parseExecArgs(cfg *execConfig, args []string) error {
	cfg.Cmd = args
	return nil
}

type execDeps struct {
	IO  cli.IO
	GCS *storage.Client
}

func (d *execDeps) SetIO(io cli.IO) { d.IO = io }

func initExecDeps(ctx context.Context) (*execDeps, error) {
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCS client")
	}
	return &execDeps{GCS: gcs}, nil
}

// execHandler dispatches a command to a scratch via /scratch/exec/op/create,
// then polls /scratch/exec/op/get until the operation is Done. It tails the
// captured output (stdout+stderr, interleaved) from GCS to stdout as it
// grows: each Get poll drives the broker's worker->GCS sync, so the object
// advances at roughly the poll cadence (floored by the broker's ~5s compose
// throttle). Output therefore arrives in chunks, not byte-live. Exec status
// and the exit code go to stderr. A terminal operation error (infra failure,
// timeout) is returned as an error; a non-zero command exit code is reported
// but not treated as a ctl failure.
func execHandler(ctx context.Context, cfg execConfig, deps *execDeps) (*act.NoOutput, error) {
	client, apiURL, err := dialAPI(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	createStub := api.Stub[schema.ScratchExecRequest, longrunning.Operation[schema.ScratchExecResult]](
		client, apiURL.JoinPath("scratch", "exec", "op", "create"))
	op, err := createStub(ctx, schema.ScratchExecRequest{
		ScratchID:      cfg.ScratchID,
		Cmd:            cfg.Cmd,
		Cwd:            cfg.Cwd,
		TimeoutSeconds: cfg.TimeoutSeconds,
		WaitSeconds:    cfg.WaitSeconds,
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating exec")
	}
	fmt.Fprintf(deps.IO.Err, "exec %s dispatched to scratch %s\n", op.ID, cfg.ScratchID)

	// printed tracks how many output bytes we've already emitted, so each
	// tail reads only the new suffix appended since the last poll.
	var printed int64
	tail := func() {
		if op.Result == nil || op.Result.OutURI == "" {
			return
		}
		n, err := tailGCSObject(ctx, deps.GCS, op.Result.OutURI, printed, deps.IO.Out)
		if err != nil {
			fmt.Fprintf(deps.IO.Err, "reading output %s: %v\n", op.Result.OutURI, err)
			return
		}
		printed += n
	}

	getStub := api.Stub[schema.GetOperationRequest, longrunning.Operation[schema.ScratchExecResult]](
		client, apiURL.JoinPath("scratch", "exec", "op", "get"))
	for !op.Done {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cfg.PollInterval):
		}
		op, err = getStub(ctx, schema.GetOperationRequest{ID: op.ID})
		if err != nil {
			return nil, errors.Wrap(err, "polling exec")
		}
		tail()
	}
	// The terminal sync may have appended the final chunk after our last
	// poll-time tail; flush whatever remains.
	tail()

	if op.Error != nil {
		return nil, errors.Errorf("exec %s failed: [%d] %s", op.ID, op.Error.Code, op.Error.Message)
	}
	fmt.Fprintf(deps.IO.Err, "exit code: %d\n", op.Result.ExitCode)
	return &act.NoOutput{}, nil
}

func execCommand() *cobra.Command {
	cfg := execConfig{}
	cmd := &cobra.Command{
		Use:   "exec --api <URI> --scratch-id <id> [flags] -- <cmd> [args...]",
		Short: "Run a command on a scratch VM and stream its output",
		Args:  cobra.MinimumNArgs(1),
		RunE:  cli.RunE(&cfg, parseExecArgs, initExecDeps, execHandler),
	}
	cmd.Flags().StringVar(&cfg.API, "api", "", "agent-api broker endpoint URI")
	cmd.Flags().StringVar(&cfg.ScratchID, "scratch-id", "", "scratch ID returned by 'scratch start'")
	cmd.Flags().StringVar(&cfg.Cwd, "cwd", "", "working directory for the command (default: worker's configured workdir)")
	cmd.Flags().IntVar(&cfg.TimeoutSeconds, "timeout-seconds", 0, "worker-enforced command timeout; 0 uses the broker default")
	cmd.Flags().IntVar(&cfg.WaitSeconds, "wait-seconds", 30, "seconds the create call waits inline for completion before falling back to polling (broker caps this at 30s)")
	cmd.Flags().DurationVar(&cfg.PollInterval, "poll-interval", 3*time.Second, "interval between op-status polls while the command runs")
	return cmd
}
