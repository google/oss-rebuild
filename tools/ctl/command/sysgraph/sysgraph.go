// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysgraph

import (
	"github.com/spf13/cobra"
)

// Command returns the sysgraph parent command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sysgraph",
		Short: "Sysgraph analysis and transformation commands",
	}
	return cmd
}
