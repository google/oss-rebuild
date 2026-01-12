// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	stabcli "github.com/google/oss-rebuild/pkg/stabilize/cli"
)

func main() {
	if err := stabcli.Command().Execute(); err != nil {
		os.Exit(1)
	}
}
