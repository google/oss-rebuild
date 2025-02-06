// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

// A message is a request/response type, used in api.Stub
type Message interface {
	Validate() error
}
