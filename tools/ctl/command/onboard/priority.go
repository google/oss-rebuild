// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import "github.com/spf13/cobra"

// priorityCommand groups the jobs that rank onboarding candidates.
func priorityCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "priority",
		Short: "Compute and export the signals that rank onboarding candidates",
	}
	cmd.AddCommand(prevalenceCommand())
	return cmd
}
