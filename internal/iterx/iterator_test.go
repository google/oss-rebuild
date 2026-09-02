// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package iterx

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCollectStopsAtFirstError(t *testing.T) {
	boom := errors.New("boom")
	seq := func(yield func(int, error) bool) {
		if !yield(1, nil) || !yield(0, boom) {
			return
		}
		t.Error("sequence continued past its error")
	}
	if got, err := Collect(seq); !errors.Is(err, boom) || got != nil {
		t.Errorf("Collect = (%v, %v), want (nil, boom)", got, err)
	}
	if got, err := Collect(FromSlice([]int{1, 2})); err != nil || !cmp.Equal(got, []int{1, 2}) {
		t.Errorf("Collect(FromSlice) = (%v, %v), want [1 2]", got, err)
	}
	if got, err := Collect(Error[int](boom)); !errors.Is(err, boom) || got != nil {
		t.Errorf("Collect(Error) = (%v, %v), want (nil, boom)", got, err)
	}
}
