// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package syncx

import (
	"iter"
	"sync"
)

// Map is a type-safe wrapper around sync.Map for general use.
// It also has some fun additions like enumeration, key, and value iterators.
type Map[K comparable, V any] struct {
	m sync.Map
}

// Clear deletes all the entries, resulting in an empty Map.
func (m *Map[K, V]) Clear() {
	m.m.Clear()
}

// Delete deletes the value for a key.
func (m *Map[K, V]) Delete(key K) {
	m.m.Delete(key)
}

// Load returns the value stored in the map for a key, or the zero value if no
// value is present. The ok result indicates whether value was found in the map.
func (m *Map[K, V]) Load(key K) (value V, ok bool) {
	v, ok := m.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	v, loaded := m.m.LoadAndDelete(key)
	if !loaded {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	a, loaded := m.m.LoadOrStore(key, value)
	return a.(V), loaded
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
func (m *Map[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

// Store sets the value for a key.
func (m *Map[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

// Swap swaps the value for a key and returns the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) Swap(key K, value V) (previous V, loaded bool) {
	p, loaded := m.m.Swap(key, value)
	if !loaded {
		var zero V
		return zero, false
	}
	return p.(V), true
}

// Iter returns an iterator over key-value pairs in the map.
// The iteration order is not specified and is not guaranteed to be the same from one call to the next.
func (m *Map[K, V]) Iter() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		m.m.Range(func(key, value any) bool {
			return yield(key.(K), value.(V))
		})
	}
}

// Keys returns an iterator over the keys in the map.
func (m *Map[K, V]) Keys() iter.Seq[K] {
	return func(yield func(K) bool) {
		m.m.Range(func(key, value any) bool {
			return yield(key.(K))
		})
	}
}

// Values returns an iterator over the values in the map.
func (m *Map[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		m.m.Range(func(key, value any) bool {
			return yield(value.(V))
		})
	}
}

// ComparableMap is a type-safe wrapper around sync.Map where both keys and values
// are comparable, enabling compare-and-swap/delete operations.
type ComparableMap[K comparable, V comparable] struct {
	Map[K, V]
}

// CompareAndDelete deletes the entry for key if its value is equal to old.
func (m *ComparableMap[K, V]) CompareAndDelete(key K, old V) (deleted bool) {
	return m.m.CompareAndDelete(key, old)
}

// CompareAndSwap swaps the old and new values for key
// if the value stored in the map is equal to old.
func (m *ComparableMap[K, V]) CompareAndSwap(key K, old, new V) (swapped bool) {
	return m.m.CompareAndSwap(key, old, new)
}
