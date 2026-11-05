// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package cache provides an interface and implementations for caching.
package cache

import (
	"sync"

	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/pkg/errors"
)

// Cache is a simple interface defining a cache.
type Cache[K comparable, V any] interface {
	Get(K) (V, error)
	Set(K, func() (V, error)) error
	GetOrSet(K, func() (V, error)) (V, error)
	Del(K)
	Clear()
}

// ErrNotExist is returned when a key does not exist in the cache.
var ErrNotExist = errors.New("does not exist")

// CoalescingMemoryCache is a simple cache that coalesces concurrent requests for the same key.
type CoalescingMemoryCache[K comparable, V any] struct {
	data syncx.ComparableMap[K, *fn[V]] // key -> sync.OnceValues
}

// fn is a wrapper that allows making func() comparable.
type fn[V any] struct {
	Func func() (V, error)
}

func (c *CoalescingMemoryCache[K, V]) valueOrClear(key K, once *fn[V]) (V, error) {
	val, err := once.Func()
	if err != nil {
		c.data.CompareAndDelete(key, once)
	}
	return val, err
}

// Get returns the value for the given key.
func (c *CoalescingMemoryCache[K, V]) Get(key K) (V, error) {
	once, ok := c.data.Load(key)
	if !ok {
		var zero V
		return zero, ErrNotExist
	}
	return c.valueOrClear(key, once)
}

// Set sets the value for the given key with the returned value from fetch.
func (c *CoalescingMemoryCache[K, V]) Set(key K, fetch func() (V, error)) error {
	once := &fn[V]{sync.OnceValues(fetch)}
	c.data.Store(key, once)
	_, err := c.valueOrClear(key, once)
	return err
}

// GetOrSet returns the value for the given key, or sets it if it does not exist.
// Notably, this will coalesce simultaneous accesses to the same key.
func (c *CoalescingMemoryCache[K, V]) GetOrSet(key K, fetch func() (V, error)) (V, error) {
	once, _ := c.data.LoadOrStore(key, &fn[V]{sync.OnceValues(fetch)})
	return c.valueOrClear(key, once)
}

// Del deletes the value for the given key.
func (c *CoalescingMemoryCache[K, V]) Del(key K) {
	c.data.Delete(key)
}

// Clear clears the cache.
func (c *CoalescingMemoryCache[K, V]) Clear() {
	c.data.Clear()
}

var _ Cache[string, any] = &CoalescingMemoryCache[string, any]{}
