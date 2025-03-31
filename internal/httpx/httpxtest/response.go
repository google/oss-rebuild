// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpxtest

import (
	"bytes"
	"io"
)

func Body(b string) io.ReadCloser {
	return io.NopCloser(bytes.NewReader([]byte(b)))
}
