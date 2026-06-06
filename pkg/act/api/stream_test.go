// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/urlx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type StreamEvent struct {
	N int `json:"n"`
}

func TestStreamHandler_HappyPath(t *testing.T) {
	handler := func(_ context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {
			for i := 1; i <= 3; i++ {
				if !yield(&StreamEvent{N: i}, nil) {
					return
				}
			}
		}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	var got []int
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got = append(got, ev.N)
	}
	if want := []int{1, 2, 3}; !equalInts(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestStreamHandler_EmptyStream(t *testing.T) {
	handler := func(_ context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	count := 0
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			t.Fatalf("unexpected error: %v (event=%v)", err, ev)
		}
		count++
	}
	if count != 0 {
		t.Errorf("got %d events, want 0", count)
	}
}

func TestStreamHandler_NilSeq(t *testing.T) {
	handler := func(_ context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return nil
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	count := 0
	for _, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("got %d events, want 0", count)
	}
}

func TestStreamHandler_MidStreamError(t *testing.T) {
	handler := func(_ context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {
			if !yield(&StreamEvent{N: 1}, nil) {
				return
			}
			if !yield(&StreamEvent{N: 2}, nil) {
				return
			}
			yield(nil, AsStatus(codes.Aborted, errors.New("boom")))
		}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	var (
		got    []int
		gotErr error
	)
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			gotErr = err
			break
		}
		got = append(got, ev.N)
	}
	if want := []int{1, 2}; !equalInts(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	st, ok := status.FromError(gotErr)
	if !ok {
		t.Fatalf("err = %v (%T), want gRPC status", gotErr, gotErr)
	}
	if st.Code() != codes.Aborted {
		t.Errorf("code = %v, want %v", st.Code(), codes.Aborted)
	}
	if st.Message() != "boom" {
		t.Errorf("message = %q, want %q", st.Message(), "boom")
	}
}

func TestStreamHandler_PreStreamError(t *testing.T) {
	// First yield is (nil, err). Framework treats this as a unary error
	// response and returns the proper HTTP status.
	handler := func(_ context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {
			yield(nil, AsStatus(codes.NotFound, errors.New("nope")))
		}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	// Raw HTTP probe to verify the wire shape (status code + Status body).
	resp, err := http.PostForm(server.URL, map[string][]string{"foo": {"foo"}})
	if err != nil {
		t.Fatalf("PostForm: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	body, _ := io.ReadAll(resp.Body)
	st := parseStatusBody(t, body)
	if st.Code() != codes.NotFound {
		t.Errorf("status code = %v, want %v", st.Code(), codes.NotFound)
	}

	// And verify the stub surfaces it as the first (nil, err) pair, with no
	// data frames.
	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)
	var dataCount int
	var stubErr error
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			stubErr = err
			break
		}
		_ = ev
		dataCount++
	}
	if dataCount != 0 {
		t.Errorf("data frames = %d, want 0", dataCount)
	}
	stubSt, ok := status.FromError(stubErr)
	if !ok {
		t.Fatalf("err = %v (%T), want gRPC status", stubErr, stubErr)
	}
	if stubSt.Code() != codes.NotFound {
		t.Errorf("stub code = %v, want %v", stubSt.Code(), codes.NotFound)
	}
}

type badInput struct {
	Foo string `form:",required"`
}

func (b badInput) Validate() error { return errors.New("nope") }

func TestStreamHandler_ValidateFailure(t *testing.T) {
	handler := func(_ context.Context, _ badInput, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		t.Errorf("handler should not be invoked when Validate fails")
		return nil
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	resp, err := http.PostForm(server.URL, map[string][]string{"foo": {"foo"}})
	if err != nil {
		t.Fatalf("PostForm: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStreamHandler_ClientBreakCancelsCtx(t *testing.T) {
	// The handler yields frames forever until ctx is cancelled. When the
	// client breaks out of the range, the server's ctx becomes Done and the
	// handler observes ctx.Err() on its next iteration.
	handlerCtxErr := make(chan error, 1)
	handler := func(ctx context.Context, _ FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {
			defer func() { handlerCtxErr <- ctx.Err() }()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if !yield(&StreamEvent{N: i}, nil) {
					return
				}
				// Small pause so the test can break out predictably.
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	got := 0
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = ev
		got++
		if got >= 3 {
			break
		}
	}
	select {
	case err := <-handlerCtxErr:
		if err == nil {
			t.Errorf("handler ctx.Err() = nil, want non-nil after client break")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handler did not unwind within 2s")
	}
}

func TestStreamHandler_EOFWithoutDone(t *testing.T) {
	// Raw HTTP handler: writes one data event, flushes, then closes the
	// connection without emitting the done sentinel. Client should surface
	// ErrUnavailable.
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprintf(rw, "event: data\ndata: {\"n\":1}\n\n")
		if f, ok := rw.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	var (
		got    []int
		gotErr error
	)
	for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
		if err != nil {
			gotErr = err
			break
		}
		got = append(got, ev.N)
	}
	if want := []int{1}; !equalInts(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if !errors.Is(gotErr, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", gotErr)
	}
}

func TestStreamHandler_ConcurrentClientsSeeDistinctCtx(t *testing.T) {
	// Sanity check that distinct requests get distinct iterators / contexts:
	// concurrent clients should each get their own per-request stream of
	// frames without cross-talk.
	handler := func(ctx context.Context, req FooRequest, _ *NoDeps) iter.Seq2[*StreamEvent, error] {
		return func(yield func(*StreamEvent, error) bool) {
			for i := 1; i <= 3; i++ {
				if !yield(&StreamEvent{N: i}, nil) {
					return
				}
			}
		}
	}
	server := httptest.NewServer(StreamHandler(NoDepsInit, handler))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StreamStub[FooRequest, StreamEvent](server.Client(), u)

	var wg sync.WaitGroup
	var fails atomic.Int32
	for range 4 {
		wg.Go(func() {
			var got []int
			for ev, err := range stub(t.Context(), FooRequest{Foo: "foo"}) {
				if err != nil {
					fails.Add(1)
					return
				}
				got = append(got, ev.N)
			}
			if want := []int{1, 2, 3}; !equalInts(got, want) {
				fails.Add(1)
			}
		})
	}
	wg.Wait()
	if n := fails.Load(); n > 0 {
		t.Errorf("%d concurrent clients did not receive the expected event stream", n)
	}
}

// equalInts compares two int slices for equality. Treats nil and empty as
// equal.
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
