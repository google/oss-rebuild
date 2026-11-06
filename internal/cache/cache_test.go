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
	cache := &CoalescingMemoryCache[string, string]{}

	err := cache.Set("key", func() (string, error) { return "value", nil })
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
	cache := &CoalescingMemoryCache[string, string]{}
	foo := errors.New("foo")
	err := cache.Set("key", func() (string, error) { return "", foo })
	if err != foo {
		t.Fatalf("cache.Set() failed: %v", err)
	}
	_, err = cache.Get("key")
	if err != ErrNotExist {
		t.Fatalf("cache.Get() failed: %v", err)
	}
}

func TestCoalescingMemoryCache_GetOrSet(t *testing.T) {
	cache := &CoalescingMemoryCache[string, string]{}

	want := "value"
	count := 5
	results := make(chan string, count)
	called := 0
	for range count {
		go func() {
			val, err := cache.GetOrSet("key", func() (string, error) {
				called++
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
				return want, nil
			})
			if err != nil {
				results <- ""
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

func TestCoalescingMemoryCache_GetOrSetErr(t *testing.T) {
	cache := &CoalescingMemoryCache[string, string]{}
	foo := errors.New("foo")
	if _, err := cache.GetOrSet("key", func() (string, error) { return "", foo }); err != foo {
		t.Fatalf("cache.GetOrSet() = %v, want %v", err, foo)
	}
	// The failure is not kept, so the next caller fetches again.
	val, err := cache.GetOrSet("key", func() (string, error) { return "value", nil })
	if err != nil || val != "value" {
		t.Fatalf("cache.GetOrSet() = %q, %v, want %q", val, err, "value")
	}
}
