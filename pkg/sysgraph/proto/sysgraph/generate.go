// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sysgraph contains protobuf definitions for sysgraph.
// The open source code must use protobuf newer than 2024 to have Opaque API enabled by default.
// see: https://go.dev/blog/protobuf-opaque
package sysgraph

//go:generate protoc --go_out=. --go_opt=paths=source_relative sysgraph.proto events.proto
