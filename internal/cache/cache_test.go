// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"errors"
	"math/rand"
	"testing"
	"time"
)

func TestCoalescingMemoryCache_GetSetDel(t *testing.T) {
	cache := &CoalescingMemoryCache{}

	err := cache.Set("key", func() (any, error) { return "value", nil })
	if err != nil {
		t.Fatalf("cache.Set() failed: %v", err)
	}
	val, err := cache.Get("key")
	if err != nil {
		t.Fatalf("cache.Get() failed: %v", err)
	}
	if val != "value" {
		t.Fatalf("cache.Get() returned %v, want %v", val, "value")
	}
	cache.Del("key")
	if err != nil {
		t.Fatalf("cache.Get() failed: %v", err)
	}
	if val != "value" {
		t.Fatalf("cache.Get() returned %v, want %v", val, "value")
	}
	_, err = cache.Get("key")
	if err == nil {
		t.Fatalf("cache.Get() succeeded, want error")
	}
}

func TestCoalescingMemoryCache_GetSetErr(t *testing.T) {
	cache := &CoalescingMemoryCache{}
	foo := errors.New("foo")
	err := cache.Set("key", func() (any, error) { return nil, foo })
	if err != foo {
		t.Fatalf("cache.Set() failed: %v", err)
	}
	_, err = cache.Get("key")
	if err != ErrNotExist {
		t.Fatalf("cache.Get() failed: %v", err)
	}
}

func TestCoalescingMemoryCache_GetOrSet(t *testing.T) {
	cache := &CoalescingMemoryCache{}

	want := "value"
	count := 5
	results := make(chan any, count)
	called := 0
	for range count {
		go func() {
			val, err := cache.GetOrSet("key", func() (any, error) {
				called++
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
				return want, nil
			})
			if err != nil {
				results <- nil
			} else {
				results <- val
			}
		}()
	}
	for range count {
		if got := <-results; got != want {
			t.Fatalf("results differed: want=%v,got=%v", want, got)
		}
	}
	if called != 1 {
		t.Fatalf("call count differed: want=1,got=%v", called)
	}
}
