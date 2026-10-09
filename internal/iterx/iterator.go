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

// FromSlice adapts records already in hand to a Seq2 that never errors.
func FromSlice[T any](xs []T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, x := range xs {
			if !yield(x, nil) {
				return
			}
		}
	}
}

// Error is a Seq2 that yields only err, for a source that failed before it
// could produce anything.
func Error[T any](err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		yield(zero, err)
	}
}

// Collect drains seq into a slice, stopping at the first error.
func Collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for x, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
