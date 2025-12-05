// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type Deps interface {
	SetIO(IO)
}

// ParseArgs populates an Input from positional arguments.
type ParseArgs[I act.Input] func(in *I, args []string) error

// SkipArgs is a ParseArgs that sets no arguments.
func SkipArgs[I act.Input](cfg *I, args []string) error {
	return nil
}

// RunE constructs a cobra.Command.RunE from act components.
// This function wires together:
//  1. Parsing positional arguments into the Input
//  2. Validating the Input
//  3. Initializing dependencies
//  4. Attaching IO streams to dependencies
//  5. Executing the action
func RunE[I act.Input, O any, D Deps](
	cfg *I,
	parseArgs ParseArgs[I],
	initDeps act.InitDeps[D],
	action act.Action[I, O, D],
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := parseArgs(cfg, args); err != nil {
			return err
		}
		if err := (*cfg).Validate(); err != nil {
			return err
		}
		deps, err := initDeps(cmd.Context())
		if err != nil {
			return errors.Wrap(err, "initializing dependencies")
		}
		deps.SetIO(IO{
			In:  cmd.InOrStdin(),
			Out: cmd.OutOrStdout(),
			Err: cmd.ErrOrStderr(),
		})
		_, err = action(cmd.Context(), *cfg, deps)
		return err
	}
}
