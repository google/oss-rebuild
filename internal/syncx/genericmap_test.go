// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package syncx

import (
	"maps"
	"sync"
	"testing"
)

func TestMap_BasicOperations(t *testing.T) {
	m := &Map[string, int]{}

	// Test Store and Load
	m.Store("key1", 100)

	value, ok := m.Load("key1")
	if !ok {
		t.Error("Expected key1 to exist")
	}
	if value != 100 {
		t.Errorf("Expected value 100, got %d", value)
	}

	// Test Load with non-existent key
	value, ok = m.Load("nonexistent")
	if ok {
		t.Error("Expected nonexistent key to not exist")
	}
	if value != 0 {
		t.Errorf("Expected zero value 0, got %d", value)
	}
}

func TestMap_Delete(t *testing.T) {
	m := &Map[string, int]{}

	m.Store("key1", 100)
	m.Store("key2", 200)

	m.Delete("key1")

	_, ok := m.Load("key1")
	if ok {
		t.Error("Expected key1 to be deleted")
	}

	// Delete non-existent key
	m.Delete("nonexistent")
}

func TestMap_LoadAndDelete(t *testing.T) {
	m := &Map[string, int]{}

	m.Store("key1", 100)

	value, loaded := m.LoadAndDelete("key1")
	if !loaded {
		t.Error("Expected key1 to be loaded")
	}
	if value != 100 {
		t.Errorf("Expected value 100, got %d", value)
	}

	// LoadAndDelete non-existent key
	value, loaded = m.LoadAndDelete("nonexistent")
	if loaded {
		t.Error("Expected nonexistent key to not be loaded")
	}
	if value != 0 {
		t.Errorf("Expected zero value 0, got %d", value)
	}
}

func TestMap_LoadOrStore(t *testing.T) {
	m := &Map[string, int]{}

	// Store new value
	actual, loaded := m.LoadOrStore("key1", 100)
	if loaded {
		t.Error("Expected key1 to not be loaded (new key)")
	}
	if actual != 100 {
		t.Errorf("Expected actual value 100, got %d", actual)
	}

	// Load existing value
	actual, loaded = m.LoadOrStore("key1", 200)
	if !loaded {
		t.Error("Expected key1 to be loaded (existing key)")
	}
	if actual != 100 {
		t.Errorf("Expected actual value 100 (original), got %d", actual)
	}
}

func TestMap_Swap(t *testing.T) {
	m := &Map[string, int]{}

	// Swap on non-existent key
	previous, loaded := m.Swap("key1", 100)
	if loaded {
		t.Error("Expected key1 to not be loaded (new key)")
	}
	if previous != 0 {
		t.Errorf("Expected zero value 0, got %d", previous)
	}

	// Swap on existing key
	previous, loaded = m.Swap("key1", 200)
	if !loaded {
		t.Error("Expected key1 to be loaded (existing key)")
	}
	if previous != 100 {
		t.Errorf("Expected previous value 100, got %d", previous)
	}

	// Verify new value
	value, ok := m.Load("key1")
	if !ok || value != 200 {
		t.Errorf("Expected new value 200, got %d", value)
	}
}

func TestMap_Clear(t *testing.T) {
	m := &Map[string, int]{}

	m.Store("key1", 100)
	m.Store("key2", 200)
	m.Store("key3", 300)

	m.Clear()

	_, ok := m.Load("key1")
	if ok {
		t.Error("Expected all keys to be cleared")
	}
}

func TestMap_Range(t *testing.T) {
	m := &Map[string, int]{}

	expected := map[string]int{
		"key1": 100,
		"key2": 200,
		"key3": 300,
	}

	for k, v := range expected {
		m.Store(k, v)
	}

	found := make(map[string]int)
	m.Range(func(key string, value int) bool {
		found[key] = value
		return true
	})

	if len(found) != len(expected) {
		t.Errorf("Expected %d items, got %d", len(expected), len(found))
	}

	for k, v := range expected {
		if found[k] != v {
			t.Errorf("Expected found[%s] = %d, got %d", k, v, found[k])
		}
	}

	// Test early termination
	count := 0
	m.Range(func(key string, value int) bool {
		count++
		return count < 2 // Stop after 2 iterations
	})

	if count != 2 {
		t.Errorf("Expected range to stop after 2 iterations, got %d", count)
	}
}

func TestMap_Iterators(t *testing.T) {
	m := &Map[string, int]{}

	expected := map[string]int{
		"key1": 100,
		"key2": 200,
		"key3": 300,
	}

	for k, v := range expected {
		m.Store(k, v)
	}

	// Test Iter()
	foundPairs := maps.Collect(m.Iter())

	if len(foundPairs) != len(expected) {
		t.Errorf("Expected %d pairs from Iter(), got %d", len(expected), len(foundPairs))
	}

	for k, v := range expected {
		if foundPairs[k] != v {
			t.Errorf("Expected foundPairs[%s] = %d, got %d", k, v, foundPairs[k])
		}
	}

	// Test Keys()
	foundKeys := make(map[string]bool)
	for k := range m.Keys() {
		foundKeys[k] = true
	}

	if len(foundKeys) != len(expected) {
		t.Errorf("Expected %d keys from Keys(), got %d", len(expected), len(foundKeys))
	}

	for k := range expected {
		if !foundKeys[k] {
			t.Errorf("Expected to find key %s", k)
		}
	}

	// Test Values()
	foundValues := make(map[int]bool)
	for v := range m.Values() {
		foundValues[v] = true
	}

	if len(foundValues) != len(expected) {
		t.Errorf("Expected %d values from Values(), got %d", len(expected), len(foundValues))
	}

	for _, v := range expected {
		if !foundValues[v] {
			t.Errorf("Expected to find value %d", v)
		}
	}
}

func TestMap_Concurrent(t *testing.T) {
	m := &Map[int, string]{}

	var wg sync.WaitGroup
	numGoroutines := 100
	itemsPerGoroutine := 10

	// Concurrent stores
	for i := range numGoroutines {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := range itemsPerGoroutine {
				key := start*itemsPerGoroutine + j
				m.Store(key, string(rune('A'+key%26)))
			}
		}(i)
	}

	wg.Wait()

	// Concurrent reads
	for i := range numGoroutines {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := range itemsPerGoroutine {
				key := start*itemsPerGoroutine + j
				_, ok := m.Load(key)
				if !ok {
					t.Errorf("Expected to find key %d", key)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestComparableMap_CompareAndDelete(t *testing.T) {
	m := &ComparableMap[string, int]{}

	m.Store("key1", 100)
	m.Store("key2", 200)

	// Delete with correct value
	deleted := m.CompareAndDelete("key1", 100)
	if !deleted {
		t.Error("Expected CompareAndDelete to succeed with correct value")
	}

	// Delete with incorrect value
	deleted = m.CompareAndDelete("key2", 100)
	if deleted {
		t.Error("Expected CompareAndDelete to fail with incorrect value")
	}

	// Delete non-existent key
	deleted = m.CompareAndDelete("nonexistent", 300)
	if deleted {
		t.Error("Expected CompareAndDelete to fail with non-existent key")
	}
}

func TestComparableMap_CompareAndSwap(t *testing.T) {
	m := &ComparableMap[string, int]{}

	m.Store("key1", 100)

	// Swap with correct old value
	swapped := m.CompareAndSwap("key1", 100, 200)
	if !swapped {
		t.Error("Expected CompareAndSwap to succeed with correct old value")
	}

	value, ok := m.Load("key1")
	if !ok || value != 200 {
		t.Errorf("Expected value 200 after swap, got %d", value)
	}

	// Swap with incorrect old value
	swapped = m.CompareAndSwap("key1", 100, 300)
	if swapped {
		t.Error("Expected CompareAndSwap to fail with incorrect old value")
	}

	value, ok = m.Load("key1")
	if !ok || value != 200 {
		t.Errorf("Expected value to remain 200, got %d", value)
	}

	// Swap non-existent key
	swapped = m.CompareAndSwap("nonexistent", 0, 400)
	if swapped {
		t.Error("Expected CompareAndSwap to fail with non-existent key")
	}
}

func TestMap_EdgeCases(t *testing.T) {
	m := &Map[string, *int]{}

	// Test with nil values
	var nilPtr *int
	m.Store("nil", nilPtr)

	value, ok := m.Load("nil")
	if !ok {
		t.Error("Expected to load nil value")
	}
	if value != nil {
		t.Error("Expected nil value")
	}

	// Test empty map operations
	emptyMap := &Map[string, int]{}
	count := 0
	emptyMap.Range(func(key string, value int) bool {
		count++
		return true
	})
	if count != 0 {
		t.Errorf("Expected 0 iterations on empty map, got %d", count)
	}

	// Test iterators on empty map
	for range emptyMap.Iter() {
		t.Error("Expected no iterations on empty map")
	}

	for range emptyMap.Keys() {
		t.Error("Expected no key iterations on empty map")
	}

	for range emptyMap.Values() {
		t.Error("Expected no value iterations on empty map")
	}
}
