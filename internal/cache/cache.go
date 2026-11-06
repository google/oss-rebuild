// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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
