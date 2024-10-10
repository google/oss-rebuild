// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pipe provides a simple way of applying transforms to a channel.
package pipe

// Pipe constructs a series of executions.
type Pipe[T any] struct {
	Width int
	steps []<-chan T
}

// From creates a Pipe from the given input channel.
func From[T any](in <-chan T) Pipe[T] {
	return Pipe[T]{steps: []<-chan T{in}, Width: cap(in)}
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
	return p.DoFor(func(in <-chan T, out chan<- T) {
		defer close(out)
		for t := range in {
			fn(t, out)
		}
	})
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
	return IntoFor(in, func(in <-chan T, out chan<- S) {
		defer close(out)
		for t := range in {
			fn(t, out)
		}
	})
}
