// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package iterx

import (
	"errors"
	"iter"
)

type iterish[T any] interface {
	Next() (T, error)
}

// ToSeq2 converts a Next()-style iterator into an iter.Seq2.
func ToSeq2[T any](it iterish[T], sentinel error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for {
			val, err := it.Next()
			if errors.Is(err, sentinel) {
				return // Stop iteration cleanly
			}
			// NOTE: yield() == false means the user exited the loop (break/return)
			if !yield(val, err) {
				return
			}
			// Stop iterating for all errors
			if err != nil {
				return
			}
		}
	}
}
