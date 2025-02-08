// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"io"
	"log"
)

func ScopedLogCapture(l *log.Logger, w io.Writer) func() {
	orig := l.Writer()
	mw := io.MultiWriter(orig, w)
	l.SetOutput(mw)
	return func() { l.SetOutput(orig) }
}
