package bitmap

import "math/bits"

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

// Set sets the bit at the given index to 1.
func (b *Bitmap) Set(index int) {
	if index < 0 || index >= b.size {
		return
	}
	b.bits[index/64] |= 1 << (index % 64)
}

// Clear sets the bit at the given index to 0.
func (b *Bitmap) Clear(index int) {
	if index < 0 || index >= b.size {
		return
	}
	b.bits[index/64] &^= 1 << (index % 64)
}

// Get returns the value of the bit at the given index.
func (b *Bitmap) Get(index int) bool {
	if index < 0 || index >= b.size {
		return false
	}
	return (b.bits[index/64] & (1 << (index % 64))) != 0
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
