// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ide

import "fmt"

func logPrefix(name string) string {
	return fmt.Sprintf("[%-9s]", name)
}
