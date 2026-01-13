// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package stabilize provides stabilizers for normalizing archive contents.
package stabilize

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

// Stabilizer defines a stabilization operation with applicability constraints.
type Stabilizer struct {
	Name        string
	constraints Constraints
	impl        stabilizerImpl
}

// FnFor returns the concrete implementation for the given context.
func (s Stabilizer) FnFor(ctx *StabilizationContext) StabilizerFn {
	if s.impl == nil || !s.constraints.Matches(ctx) {
		return nil
	}
	return s.impl.For(ctx)
}

// stabilizerImpl resolves to a concrete function based on context.
type stabilizerImpl interface {
	For(ctx *StabilizationContext) StabilizerFn
}

// StabilizerFn is the marker interface for stabilizer functions.
type StabilizerFn interface {
	Constraints() Constraints
}

type Constraints []Constraint

func (cs Constraints) Matches(ctx *StabilizationContext) bool {
	for _, c := range cs {
		if !c.Matches(ctx) {
			return false
		}
	}
	return true
}

type Any Constraints

func (a Any) Matches(ctx *StabilizationContext) bool {
	for _, c := range a {
		if c.Matches(ctx) {
			return true
		}
	}
	return false
}

// --- Impl wrappers ---

type singleImpl struct {
	fn StabilizerFn
}

func (s singleImpl) For(ctx *StabilizationContext) StabilizerFn {
	if s.fn.Constraints().Matches(ctx) {
		return s.fn
	}
	return nil
}

type multiImpl struct {
	fns map[archive.Format]StabilizerFn
}

func (m multiImpl) For(ctx *StabilizationContext) StabilizerFn {
	return m.fns[ctx.Format()]
}

// --- Constructors ---

// WithFn returns a Stabilizer with a single implementation.
func (s Stabilizer) WithFn(fn StabilizerFn) Stabilizer {
	if s.impl != nil {
		panic("stabilizer implementation initialized multiple times")
	}
	s.constraints = append(s.constraints, fn.Constraints())
	s.impl = singleImpl{fn: fn}
	return s
}

// WithFns returns a Stabilizer with multiple implementations for different formats.
func (s Stabilizer) WithFns(fns map[archive.Format]StabilizerFn) Stabilizer {
	if s.impl != nil {
		panic("stabilizer implementation initialized multiple times")
	}
	var a Any
	for format, fn := range fns {
		if c := fn.Constraints(); !c.Matches(NewContext(format)) {
			panic(fmt.Sprintf("invalid fn type %T for format %v", fn, format))
		} else {
			a = append(a, c)
		}
	}
	s.constraints = append(s.constraints, a)
	s.impl = multiImpl{fns: fns}
	return s
}

// WithConstraints returns a Stabilizer with the added constraints.
func (s Stabilizer) WithConstraints(c ...Constraint) Stabilizer {
	s.constraints = append(s.constraints, c...)
	return s
}

// StabilizeOpts aggregates stabilizers to be used in stabilization.
type StabilizeOpts struct {
	Stabilizers []Stabilizer
}

// Stabilize applies default stabilization to the provided archive.
func Stabilize(dst io.Writer, src io.Reader, f archive.Format) error {
	return StabilizeWithOpts(dst, src, f, StabilizeOpts{Stabilizers: AllStabilizers})
}

// StabilizeWithOpts selects and applies the provided stabilization routine for the given archive format.
func StabilizeWithOpts(dst io.Writer, src io.Reader, f archive.Format, opts StabilizeOpts) error {
	ctx := NewContext(f)
	switch f {
	case archive.ZipFormat:
		srcReader, size, err := archive.ToZipCompatibleReader(src)
		if err != nil {
			return errors.Wrap(err, "converting reader")
		}
		zr, err := zip.NewReader(srcReader, size)
		if err != nil {
			return errors.Wrap(err, "initializing zip reader")
		}
		zw := zip.NewWriter(dst)
		defer zw.Close()
		err = StabilizeZip(zr, zw, opts, ctx)
		if err != nil {
			return errors.Wrap(err, "stabilizing zip")
		}
	case archive.TarGzFormat:
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return errors.Wrap(err, "initializing gzip reader")
		}
		defer gzr.Close()
		gzw, err := NewStabilizedGzipWriter(gzr, dst, opts, ctx)
		if err != nil {
			return errors.Wrap(err, "initializing gzip writer")
		}
		defer gzw.Close()
		err = StabilizeTar(tar.NewReader(gzr), tar.NewWriter(gzw), opts, ctx)
		if err != nil {
			return errors.Wrap(err, "stabilizing tar.gz")
		}
	case archive.GzipFormat:
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return errors.Wrap(err, "initializing gzip reader")
		}
		defer gzr.Close()
		gzw, err := NewStabilizedGzipWriter(gzr, dst, opts, ctx)
		if err != nil {
			return errors.Wrap(err, "stabilizing gzip")
		}
		defer gzw.Close()
		if _, err := io.Copy(gzw, gzr); err != nil {
			return errors.Wrap(err, "copying gzip content")
		}
	case archive.TarFormat:
		err := StabilizeTar(tar.NewReader(src), tar.NewWriter(dst), opts, ctx)
		if err != nil {
			return errors.Wrap(err, "stabilizing tar")
		}
	case archive.RawFormat:
		if _, err := io.Copy(dst, src); err != nil {
			return errors.Wrap(err, "copying raw")
		}
	default:
		return errors.New("unsupported archive type")
	}
	return nil
}
