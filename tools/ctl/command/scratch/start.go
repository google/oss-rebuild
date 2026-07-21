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

type startConfig struct {
	API          string
	MachineClass string
	BuildID      string
}

func (c startConfig) Validate() error {
	if c.API == "" {
		return errors.New("--api is required")
	}
	if c.MachineClass == "" {
		return errors.New("--machine-class is required")
	}
	return nil
}

type startDeps struct{ IO cli.IO }

func (d *startDeps) SetIO(io cli.IO) { d.IO = io }

func initStartDeps(context.Context) (*startDeps, error) { return &startDeps{}, nil }

// startHandler mints a scratch VM via /scratch/create. It blocks until the
// broker reports the VM Ready (or fails), then prints the scratch ID to
// stdout (so it can be captured) and human-readable details to stderr.
func startHandler(ctx context.Context, cfg startConfig, deps *startDeps) (*act.NoOutput, error) {
	client, apiURL, err := dialAPI(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	stub := api.Stub[schema.ScratchCreateRequest, schema.Scratch](client, apiURL.JoinPath("scratch", "create"))
	fmt.Fprintf(deps.IO.Err, "minting %s scratch VM (this can take a minute)...\n", cfg.MachineClass)
	s, err := stub(ctx, schema.ScratchCreateRequest{
		BuildID:      cfg.BuildID,
		MachineClass: schema.MachineClass(cfg.MachineClass),
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating scratch")
	}
	fmt.Fprintf(deps.IO.Err, "ready: vm=%s zone=%s ip=%s class=%s state=%s\n",
		s.VMName, s.Zone, s.InternalIP, s.MachineClass, s.State)
	fmt.Fprintf(deps.IO.Err, "\n  exec: ctl scratch exec --api %s --scratch-id %s -- <cmd>...\n", cfg.API, s.ID)
	fmt.Fprintf(deps.IO.Err, "  kill: ctl scratch kill --api %s --scratch-id %s\n", cfg.API, s.ID)
	fmt.Fprintln(deps.IO.Out, s.ID)
	return &act.NoOutput{}, nil
}

func startCommand() *cobra.Command {
	cfg := startConfig{}
	cmd := &cobra.Command{
		Use:   "start --api <URI> [--machine-class standard|jumbo] [--build-id <id>]",
		Short: "Mint a scratch build VM and print its ID",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[startConfig], initStartDeps, startHandler),
	}
	cmd.Flags().StringVar(&cfg.API, "api", "", "agent-api broker endpoint URI")
	cmd.Flags().StringVar(&cfg.MachineClass, "machine-class", "standard", "VM machine class: standard or jumbo")
	cmd.Flags().StringVar(&cfg.BuildID, "build-id", "manual", "build_id recorded on the scratch (arbitrary for manual use)")
	return cmd
}
