// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import "time"

var epoch = time.UnixMilli(0)

func must[T any](t T, err error) T {
	orDie(err)
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}

func all(predicates ...bool) bool {
	for _, v := range predicates {
		if !v {
			return false
		}
	}
	return true
}
