// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package longrunning

import "fmt"

// Operation represents a long-running operation.
type Operation[R any] struct {
	ID     string          `json:"id"`
	Done   bool            `json:"done"`
	Error  *OperationError `json:"error,omitempty"`
	Result *R              `json:"result,omitempty"`
}

// OperationError represents an error that occurred during a long-running operation.
type OperationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements error.
func (e *OperationError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}
