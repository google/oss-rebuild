// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cache

import (
	"errors"
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
				time.Sleep(time.Second)
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
