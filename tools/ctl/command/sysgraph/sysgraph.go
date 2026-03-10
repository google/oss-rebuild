// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysgraph

import (
	"github.com/google/oss-rebuild/tools/ctl/command/sysgraph/annotatenetwork"
	"github.com/google/oss-rebuild/tools/ctl/command/sysgraph/diff"
	"github.com/spf13/cobra"
)

// Command returns the sysgraph parent command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sysgraph",
		Short: "Sysgraph analysis and transformation commands",
	}
	cmd.AddCommand(annotatenetwork.Command())
	cmd.AddCommand(diff.Command())
	return cmd
}
