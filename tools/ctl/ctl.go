// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log"

	"github.com/google/oss-rebuild/tools/ctl/command/agentexport"
	"github.com/google/oss-rebuild/tools/ctl/command/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/command/export"
	"github.com/google/oss-rebuild/tools/ctl/command/getgradlegav"
	"github.com/google/oss-rebuild/tools/ctl/command/getresults"
	"github.com/google/oss-rebuild/tools/ctl/command/getsessions"
	"github.com/google/oss-rebuild/tools/ctl/command/gettrackedpackages"
	"github.com/google/oss-rebuild/tools/ctl/command/infer"
	"github.com/google/oss-rebuild/tools/ctl/command/listruns"
	"github.com/google/oss-rebuild/tools/ctl/command/localagent"
	"github.com/google/oss-rebuild/tools/ctl/command/migrate"
	"github.com/google/oss-rebuild/tools/ctl/command/runagent"
	"github.com/google/oss-rebuild/tools/ctl/command/runagentbenchmark"
	"github.com/google/oss-rebuild/tools/ctl/command/runbenchmark"
	"github.com/google/oss-rebuild/tools/ctl/command/runone"
	"github.com/google/oss-rebuild/tools/ctl/command/settrackedpackages"
	"github.com/google/oss-rebuild/tools/ctl/command/tui"
	"github.com/google/oss-rebuild/tools/ctl/command/viewsession"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ctl",
	Short: "A debugging tool for OSS-Rebuild",
}

func init() {
	// Execution
	rootCmd.AddCommand(runbenchmark.Command())
	rootCmd.AddCommand(runone.Command())
	rootCmd.AddCommand(runagent.Command())
	rootCmd.AddCommand(localagent.Command())
	rootCmd.AddCommand(runagentbenchmark.Command())
	rootCmd.AddCommand(benchmark.Command())
	// Reading data
	rootCmd.AddCommand(tui.Command())
	rootCmd.AddCommand(getresults.Command())
	rootCmd.AddCommand(export.Command())
	rootCmd.AddCommand(listruns.Command())
	rootCmd.AddCommand(getsessions.Command())
	rootCmd.AddCommand(viewsession.Command())
	rootCmd.AddCommand(agentexport.Command())
	// Rebuild logic
	rootCmd.AddCommand(infer.Command())
	rootCmd.AddCommand(getgradlegav.Command())
	// Infra tools
	rootCmd.AddCommand(migrate.Command())
	rootCmd.AddCommand(settrackedpackages.Command())
	rootCmd.AddCommand(gettrackedpackages.Command())
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
