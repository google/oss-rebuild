// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package jsonl decodes and encodes JSON Lines streams (one JSON value per line).
package jsonl

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"

	"github.com/pkg/errors"
)

// Decode yields T values from r in order.
// A decode error arrives as the final pair and ends the sequence.
func Decode[T any](r io.Reader) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for dec := json.NewDecoder(r); dec.More(); {
			var v T
			err := dec.Decode(&v)
			if err != nil {
				err = errors.Wrap(err, "decoding record")
			}
			if !yield(v, err) || err != nil {
				return
			}
		}
	}
}

// DecodeAll materializes Decode's sequence into a slice.
func DecodeAll[T any](r io.Reader) ([]T, error) {
	var out []T
	for v, err := range Decode[T](r) {
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Encode writes T values as rows to w, buffering the row writes.
// Closing w remains the resposibility of the caller.
func Encode[T any](w io.Writer, rows []T) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return errors.Wrap(err, "encoding record")
		}
	}
	return errors.Wrap(bw.Flush(), "writing records")
}
