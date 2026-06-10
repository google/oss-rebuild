// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package longrunning

import "fmt"

// Operation represents a long-running operation.
//
// Unlike google.longrunning.Operation's strict oneof of {error, response},
// Result and Error here may coexist: projection functions are encouraged to
// populate Result as a snapshot of observable state (e.g. partial output URIs,
// timing metadata) and additionally set Error when the operation has terminally
// failed. Callers must check Error first to decide whether the Result data
// represents a successful outcome or partial/diagnostic state captured before
// failure. See agentapiservice.ProjectScratchExec and apiservice.ProjectRebuildAttempt
// for established uses of this pattern.
type Operation[R any] struct {
	ID     string          `json:"id"`
	Done   bool            `json:"done"`
	Error  *OperationError `json:"error,omitempty"`
	Result *R              `json:"result,omitempty"`
}

// OperationError represents an error that occurred during a long-running operation.
//
// Code is a gRPC status code (google.golang.org/grpc/codes) cast to int.
// Existing in-tree usage is gradually migrating from HTTP-status values to
// this convention; new projections should emit gRPC codes.
type OperationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements error.
func (e *OperationError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}
