// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
)

// GetOperationRequest is the request type for getting an operation.
type GetOperationRequest struct {
	ID string `json:"id" form:"id,required"`
}

// Validate validates the GetOperationRequest.
func (r GetOperationRequest) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	return nil
}
