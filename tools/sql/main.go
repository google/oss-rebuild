// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// sql is a tool to run SQL queries against run and rebuild metadata.
package main

import (
	"log"

	"github.com/google/oss-rebuild/tools/ctl/command/query"
	"github.com/spf13/cobra"
)

func main() {
	log.SetFlags(0)
	rootCmd := &cobra.Command{
		Use:   "sql",
		Short: "SQL query tools",
	}
	rootCmd.AddCommand(query.Command())
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
