// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package bitmap

import (
	"fmt"
	"math/bits"
)

// Bitmap represents a bitmap data structure.
type Bitmap struct {
	bits []uint64
	size int
}

// New creates a new Bitmap with the given size.
func New(size int) *Bitmap {
	return &Bitmap{
		bits: make([]uint64, (size+63)/64),
		size: size,
	}
}

// NewBatch creates "count" new Bitmaps with the given size
func NewBatch(size, count int) []Bitmap {
	mapints := (size + 63) / 64
	mem := make([]uint64, count*mapints)
	var maps []Bitmap
	for i := range count {
		maps = append(maps, Bitmap{
			bits: mem[i*mapints : (i+1)*mapints],
			size: size,
		})
	}
	return maps
}

// Set sets the bit at the given index to 1. It panics if index is out of range.
func (b *Bitmap) Set(index int) {
	b.check(index)
	b.bits[index/64] |= 1 << (index % 64)
}

// Clear sets the bit at the given index to 0. It panics if index is out of range.
func (b *Bitmap) Clear(index int) {
	b.check(index)
	b.bits[index/64] &^= 1 << (index % 64)
}

// Get returns the value of the bit at the given index. It panics if index is
// out of range.
func (b *Bitmap) Get(index int) bool {
	b.check(index)
	return (b.bits[index/64] & (1 << (index % 64))) != 0
}

// check panics on an index outside the bitmap. An index past the size but
// inside the last word would otherwise set a bit that Count reports.
func (b *Bitmap) check(index int) {
	if index < 0 || index >= b.size {
		panic(fmt.Sprintf("bitmap index %d out of range [0, %d)", index, b.size))
	}
}

// Count returns the number of set bits in the bitmap.
func (b *Bitmap) Count() int {
	count := 0
	for _, x := range b.bits {
		count += bits.OnesCount64(x)
	}
	return count
}

// Size returns the size of the bitmap.
func (b *Bitmap) Size() int {
	return b.size
}

// Or performs a bitwise OR operation with another bitmap, modifying this bitmap.
func (b *Bitmap) Or(other *Bitmap) {
	if b.size != other.size {
		panic("size mismatch")
	}
	for i := range b.bits {
		b.bits[i] |= other.bits[i]
	}
}

// And performs a bitwise AND operation with another bitmap, modifying this bitmap.
func (b *Bitmap) And(other *Bitmap) {
	if b.size != other.size {
		panic("size mismatch")
	}
	for i := range b.bits {
		b.bits[i] &= other.bits[i]
	}
}

// Xor performs a bitwise XOR operation with another bitmap, modifying this bitmap.
func (b *Bitmap) Xor(other *Bitmap) {
	if b.size != other.size {
		panic("size mismatch")
	}
	for i := range b.bits {
		b.bits[i] ^= other.bits[i]
	}
}

// Not performs a bitwise NOT operation on this bitmap, modifying it.
func (b *Bitmap) Not() {
	for i := range b.bits {
		b.bits[i] = ^b.bits[i]
	}
	// Clear any bits beyond the size of the bitmap
	if b.size%64 != 0 {
		b.bits[len(b.bits)-1] &= (1<<(b.size%64) - 1)
	}
}
