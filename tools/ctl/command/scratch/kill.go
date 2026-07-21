// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"fmt"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type killConfig struct {
	API       string
	ScratchID string
}

func (c killConfig) Validate() error {
	if c.API == "" {
		return errors.New("--api is required")
	}
	if c.ScratchID == "" {
		return errors.New("--scratch-id is required")
	}
	return nil
}

type killDeps struct{ IO cli.IO }

func (d *killDeps) SetIO(io cli.IO) { d.IO = io }

func initKillDeps(context.Context) (*killDeps, error) { return &killDeps{}, nil }

// killHandler tears down a scratch's GCE resources via /scratch/delete. The
// broker marks the record Deleted (kept for audit) and best-effort deletes
// the VM.
func killHandler(ctx context.Context, cfg killConfig, deps *killDeps) (*act.NoOutput, error) {
	client, apiURL, err := dialAPI(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	stub := api.Stub[schema.ScratchDeleteRequest, schema.ScratchDeleteResponse](client, apiURL.JoinPath("scratch", "delete"))
	resp, err := stub(ctx, schema.ScratchDeleteRequest{ScratchID: cfg.ScratchID})
	if err != nil {
		return nil, errors.Wrap(err, "deleting scratch")
	}
	fmt.Fprintf(deps.IO.Err, "scratch %s: %s\n", resp.ScratchID, resp.State)
	return &act.NoOutput{}, nil
}

func killCommand() *cobra.Command {
	cfg := killConfig{}
	cmd := &cobra.Command{
		Use:   "kill --api <URI> --scratch-id <id>",
		Short: "Tear down a scratch VM",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[killConfig], initKillDeps, killHandler),
	}
	cmd.Flags().StringVar(&cfg.API, "api", "", "agent-api broker endpoint URI")
	cmd.Flags().StringVar(&cfg.ScratchID, "scratch-id", "", "scratch ID returned by 'scratch start'")
	return cmd
}
