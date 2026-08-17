// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package onboard implements the commands that bring packages into oss-rebuild coverage.
package onboard

import (
	"context"

	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/spf13/cobra"
)

// Deps is shared by all onboard subcommands.
type Deps struct{ IO cli.IO }

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes shared dependencies.
func InitDeps(context.Context) (*Deps, error) { return &Deps{}, nil }

// Command returns the parent `onboard` command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Bring packages into rebuild coverage",
	}
	cmd.AddCommand(priorityCommand())
	return cmd
}
