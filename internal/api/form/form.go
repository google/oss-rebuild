// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package form

import (
	actform "github.com/google/oss-rebuild/pkg/act/api/form"
)

// Re-exports for backwards compatibility
var (
	ErrInvalidType      = actform.ErrInvalidType
	ErrUnsupportedField = actform.ErrUnsupportedField
	ErrMissingRequired  = actform.ErrMissingRequired

	Marshal   = actform.Marshal
	Unmarshal = actform.Unmarshal
)
