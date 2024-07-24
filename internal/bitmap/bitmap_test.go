package bitmap

import (
	"testing"
)

func TestNew(t *testing.T) {
	b := New(100)
	if b.Size() != 100 {
		t.Errorf("Expected size 100, got %d", b.Size())
	}
	if len(b.bits) != 2 {
		t.Errorf("Expected 2 uint64s, got %d", len(b.bits))
	}
}

func TestNewBatch(t *testing.T) {
	bitmaps := NewBatch(100, 5)
	if len(bitmaps) != 5 {
		t.Errorf("Expected 5 bitmaps, got %d", len(bitmaps))
	}
	for i, b := range bitmaps {
		if b.Size() != 100 {
			t.Errorf("Bitmap %d: Expected size 100, got %d", i, b.Size())
		}
		if len(b.bits) != 2 {
			t.Errorf("Bitmap %d: Expected 2 uint64s, got %d", i, len(b.bits))
		}
	}
}

func TestSetGetClear(t *testing.T) {
	b := New(100)
	b.Set(50)
	if !b.Get(50) {
		t.Error("Expected bit 50 to be set")
	}
	b.Clear(50)
	if b.Get(50) {
		t.Error("Expected bit 50 to be cleared")
	}
}

func TestSetGetClearOutOfRange(t *testing.T) {
	b := New(100)
	b.Set(200)   // Should not panic
	b.Clear(200) // Should not panic
	if b.Get(200) {
		t.Error("Expected out of range Get to return false")
	}
}

func TestCount(t *testing.T) {
	b := New(100)
	b.Set(10)
	b.Set(20)
	b.Set(30)
	if b.Count() != 3 {
		t.Errorf("Expected count 3, got %d", b.Count())
	}
}

func TestOr(t *testing.T) {
	b1 := New(100)
	b2 := New(100)
	b1.Set(10)
	b2.Set(20)
	b1.Or(b2)
	if !b1.Get(10) || !b1.Get(20) {
		t.Error("OR operation failed")
	}
}

func TestAnd(t *testing.T) {
	b1 := New(100)
	b2 := New(100)
	b1.Set(10)
	b1.Set(20)
	b2.Set(20)
	b2.Set(30)
	b1.And(b2)
	if b1.Get(10) || !b1.Get(20) || b1.Get(30) {
		t.Error("AND operation failed")
	}
}

func TestXor(t *testing.T) {
	b1 := New(100)
	b2 := New(100)
	b1.Set(10)
	b1.Set(20)
	b2.Set(20)
	b2.Set(30)
	b1.Xor(b2)
	if !b1.Get(10) || b1.Get(20) || !b1.Get(30) {
		t.Error("XOR operation failed")
	}
}

func TestNot(t *testing.T) {
	b := New(100)
	b.Set(10)
	b.Not()
	if b.Get(10) || !b.Get(11) {
		t.Error("NOT operation failed")
	}
}

func TestNotWithNonMultipleOf64(t *testing.T) {
	b := New(100)
	b.Not()
	if b.Count() != 100 {
		t.Error("NOT operation failed to invert values")
	}
}

func TestLargeBitmap(t *testing.T) {
	b := New(1000000)
	b.Set(999999)
	if !b.Get(999999) {
		t.Error("Failed to set/get bit in large bitmap")
	}
}
