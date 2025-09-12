// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package pipe provides a simple way of applying transforms to a channel.
package pipe

import (
	"math"
	"sync"
)

// Pipe constructs a series of executions.
type Pipe[T any] struct {
	Width int
	steps []<-chan T
}

// From creates a Pipe from the given input channel.
func From[T any](in <-chan T) Pipe[T] {
	return Pipe[T]{steps: []<-chan T{in}, Width: cap(in)}
}

// FromSlice creates a Pipe from the given input slice.
func FromSlice[T any](in []T) Pipe[T] {
	out := make(chan T)
	go func() {
		for _, t := range in {
			out <- t
		}
		close(out)
	}()
	return From(out)
}

// DoFor adds a pipeline combinator.
// NOTE: fn is responsible for closing "in".
func (p Pipe[T]) DoFor(fn func(in <-chan T, out chan<- T)) Pipe[T] {
	next := make(chan T, p.Width)
	go fn(p.steps[len(p.steps)-1], next)
	p.steps = append(p.steps, next)
	return p
}

// Do adds a per-item combinator.
func (p Pipe[T]) Do(fn func(in T, out chan<- T)) Pipe[T] {
	return p.DoFor(do(fn))
}

// ParDo adds an out-of-order, concurrent pipeline combinator.
func (p Pipe[T]) ParDo(concurrency int, fn func(in T, out chan<- T)) Pipe[T] {
	return p.DoFor(parDo(concurrency, fn))
}

// Out produces the final output channel.
func (p Pipe[T]) Out() <-chan T {
	return p.steps[len(p.steps)-1]
}

// IntoFor takes the input pipe and transforms it to another type.
func IntoFor[T, S any](in Pipe[T], fn func(in <-chan T, out chan<- S)) Pipe[S] {
	next := make(chan S, in.Width)
	go fn(in.steps[len(in.steps)-1], next)
	out := From(next)
	return out
}

// Into takes the input pipe and transforms it to another type.
func Into[T, S any](in Pipe[T], fn func(in T, out chan<- S)) Pipe[S] {
	return IntoFor(in, do(fn))
}

// ParInto takes the input pipe and transforms it to another type in parallel.
func ParInto[T, S any](concurrency int, in Pipe[T], fn func(in T, out chan<- S)) Pipe[S] {
	return IntoFor(in, parDo(concurrency, fn))
}

func do[T, S any](fn func(in T, out chan<- S)) func(in <-chan T, out chan<- S) {
	return func(in <-chan T, out chan<- S) {
		defer close(out)
		for t := range in {
			fn(t, out)
		}
	}
}

func parDo[T, S any](concurrency int, fn func(in T, out chan<- S)) func(in <-chan T, out chan<- S) {
	return func(in <-chan T, out chan<- S) {
		defer close(out)
		if concurrency < 0 {
			concurrency = math.MaxInt
		}
		bucket := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for t := range in {
			wg.Add(1)
			bucket <- struct{}{}
			go func() {
				defer wg.Done()
				fn(t, out)
				<-bucket
			}()
		}
		wg.Wait()
	}
}
