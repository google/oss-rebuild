// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package safememfs

import (
	"os"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
)

// SafeMemory is a thread-safe wrapper for any billy.Filesystem.
type SafeMemory struct {
	fs billy.Filesystem
	mu *sync.RWMutex
}

// New creates a new thread-safe in-memory filesystem.
func New() *SafeMemory {
	return &SafeMemory{
		fs: memfs.New(),
		mu: &sync.RWMutex{},
	}
}

func (s *SafeMemory) Chroot(path string) (billy.Filesystem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	newFs, err := s.fs.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &SafeMemory{
		fs: newFs,
		mu: s.mu, // NOTE: same mutex
	}, nil
}

func (s *SafeMemory) Root() string {
	return "/"
}

// --- Write Operations (use exclusive Lock) ---

func (s *SafeMemory) Create(filename string) (billy.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.Create(filename)
}

func (s *SafeMemory) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.OpenFile(filename, flag, perm)
}

func (s *SafeMemory) MkdirAll(path string, perm os.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.MkdirAll(path, perm)
}

func (s *SafeMemory) Rename(from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.Rename(from, to)
}

func (s *SafeMemory) Remove(filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.Remove(filename)
}

func (s *SafeMemory) Symlink(target, link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.Symlink(target, link)
}

func (s *SafeMemory) TempFile(dir, prefix string) (billy.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fs.TempFile(dir, prefix)
}

// --- Read Operations (use shared RLock) ---

func (s *SafeMemory) Open(filename string) (billy.File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fs.OpenFile(filename, os.O_RDONLY, 0)
}

func (s *SafeMemory) Stat(filename string) (os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fs.Stat(filename)
}

func (s *SafeMemory) Lstat(filename string) (os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fs.Lstat(filename)
}

func (s *SafeMemory) ReadDir(path string) ([]os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fs.ReadDir(path)
}

func (s *SafeMemory) Readlink(link string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fs.Readlink(link)
}

func (s *SafeMemory) Join(elem ...string) string {
	// No lock needed as this is a pure function
	return s.fs.Join(elem...)
}

// Capabilities forwards the call to the underlying filesystem.
// No lock needed as it doesn't modify state.
func (s *SafeMemory) Capabilities() billy.Capability {
	if capable, ok := s.fs.(billy.Capable); ok {
		return capable.Capabilities()
	}
	// Default capabilities if the underlying fs doesn't implement billy.Capable
	return 0
}

// Ensure the wrapper implements the full interface.
var _ billy.Filesystem = (*SafeMemory)(nil)
var _ billy.Capable = (*SafeMemory)(nil)
