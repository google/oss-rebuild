// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build tools

package tools

import (
	// used by regenrdb.go, need this file here to hint go mod tidy to include this dependency.
	_ "github.com/protocolbuffers/txtpbfmt/parser"
)
