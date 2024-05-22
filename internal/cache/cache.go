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

// Package cache provides an interface and implementations for caching.
package cache

import (
	"sync"

	"github.com/pkg/errors"
)

// Cache is a simple interface defining a cache.
type Cache interface {
	Get(any) (any, error)
	Set(any, func() (any, error)) error
	GetOrSet(any, func() (any, error)) (any, error)
	Del(any)
	Clear()
}

// ErrNotExist is returned when a key does not exist in the cache.
var ErrNotExist = errors.New("does not exist")

// CoalescingMemoryCache is a simple cache that coalesces concurrent requests for the same key.
type CoalescingMemoryCache struct {
	data sync.Map // key -> sync.OnceValues
}

// fn is a wrapper that allows making func() comparable.
type fn struct {
	Func func() (any, error)
}

func (c *CoalescingMemoryCache) valueOrClear(key, once any) (any, error) {
	val, err := once.(*fn).Func()
	if err != nil {
		c.data.CompareAndDelete(key, once)
	}
	return val, err
}

// Get returns the value for the given key.
func (c *CoalescingMemoryCache) Get(key any) (any, error) {
	once, ok := c.data.Load(key)
	if !ok {
		return nil, ErrNotExist
	}
	return c.valueOrClear(key, once)
}

// Set sets the value for the given key with the returned value from fetch.
func (c *CoalescingMemoryCache) Set(key any, fetch func() (any, error)) error {
	once := &fn{sync.OnceValues(fetch)}
	c.data.Store(key, once)
	_, err := c.valueOrClear(key, once)
	return err
}

// GetOrSet returns the value for the given key, or sets it if it does not exist.
// Notably, this will coalesce simultaneous accesses to the same key.
func (c *CoalescingMemoryCache) GetOrSet(key any, fetch func() (any, error)) (any, error) {
	once, _ := c.data.LoadOrStore(key, &fn{sync.OnceValues(fetch)})
	return c.valueOrClear(key, once)
}

// Del deletes the value for the given key.
func (c *CoalescingMemoryCache) Del(key any) {
	c.data.Delete(key)
}

// Clear clears the cache.
func (c *CoalescingMemoryCache) Clear() {
	c.data = sync.Map{}
}

var _ Cache = &CoalescingMemoryCache{}

// HierarchicalCache is a cache that can be composed of multiple caches.
// It will search the caches in order, and if a value is not found, it will
// set the value in the most recently Push'd (i.e. "nearest") cache.
// NOTE: Modifications (e.g. Set, Del, Clear) only apply to the nearest cache.
// Consequently, all other caches are read-only while lower in the stack.
type HierarchicalCache struct {
	stack []Cache
	// NOTE: This protects []Cache itself, not the Cache objects. "Readers" are
	// those reading the slice while "Writers" are modifying the slice (i.e.
	// adding or removing elements).
	m sync.RWMutex
}

// NewHierarchicalCache creates a new HierarchicalCache with the given base cache.
func NewHierarchicalCache(base Cache) *HierarchicalCache {
	return &HierarchicalCache{[]Cache{base}, sync.RWMutex{}}
}

func (h *HierarchicalCache) get(key any) (any, error) {
	for i := range h.stack {
		c := h.stack[len(h.stack)-1-i]
		if val, err := c.Get(key); err == nil {
			return val, nil
		} else if err != ErrNotExist {
			return nil, err
		}
	}
	return nil, ErrNotExist
}

// Get returns the value for the given key, or ErrNotExist if it does not exist.
func (h *HierarchicalCache) Get(key any) (any, error) {
	h.m.RLock()
	defer h.m.RUnlock()
	return h.get(key)
}

// Set sets the value for the given key in the nearest cache.
func (h *HierarchicalCache) Set(key any, fetch func() (any, error)) error {
	h.m.RLock()
	defer h.m.RUnlock()
	return h.stack[len(h.stack)-1].Set(key, fetch)
}

// GetOrSet returns the value for the given key, or sets it if it does not exist.
// This will coalesce simultaneous accesses to the same key.
func (h *HierarchicalCache) GetOrSet(key any, fetch func() (any, error)) (any, error) {
	h.m.RLock()
	defer h.m.RUnlock()
	if val, err := h.get(key); err == nil {
		return val, nil
	} else if err != ErrNotExist {
		return nil, err
	}
	return h.stack[len(h.stack)-1].GetOrSet(key, fetch)
}

// Del deletes the value for the given key from the nearest cache.
func (h *HierarchicalCache) Del(key any) {
	h.m.RLock()
	defer h.m.RUnlock()
	h.stack[len(h.stack)-1].Del(key)
}

// Clear clears the nearest cache.
func (h *HierarchicalCache) Clear() {
	h.m.RLock()
	defer h.m.RUnlock()
	h.stack[len(h.stack)-1].Clear()
}

// Push adds a new cache to the top of the stack.
func (h *HierarchicalCache) Push(c Cache) {
	h.m.Lock()
	defer h.m.Unlock()
	h.stack = append(h.stack, c)
}

// Pop removes the nearest cache from the stack.
func (h *HierarchicalCache) Pop() error {
	h.m.Lock()
	defer h.m.Unlock()
	if len(h.stack) == 1 {
		return errors.New("cannot pop last level cache")
	}
	h.stack = h.stack[:len(h.stack)-1]
	return nil
}

var _ Cache = &HierarchicalCache{}
