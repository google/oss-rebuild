// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/internal/urlx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FooRequest struct {
	Foo string `form:",required"`
}

func (FooRequest) Validate() error { return nil }

type FooResponse struct {
	Bar string
}

func TestNoDepsInit(t *testing.T) {
	ctx := context.Background()
	deps, err := NoDepsInit(ctx)
	if err != nil {
		t.Errorf("NoDepsInit returned an error: %v", err)
	}
	if deps == nil {
		t.Error("NoDepsInit returned nil deps")
	}
}

func TestStub(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm(): %v", err)
		}
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if form := r.Form.Encode(); form != "foo=foo" {
			t.Errorf("Expected form 'foo=foo', got '%s'", form)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"Bar":"Bar"}`))
	}))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := Stub[FooRequest, FooResponse](server.Client(), u)

	ctx := context.Background()
	result, err := stub(ctx, FooRequest{Foo: "foo"})

	if err != nil {
		t.Errorf("Stub returned an error: %v", err)
	}
	expected := &FooResponse{Bar: "Bar"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestStubFromHandler(t *testing.T) {
	h := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		if req.Foo != "foo" {
			t.Errorf("request.Foo: want='foo' got='%s'", req.Foo)
		}
		return &FooResponse{Bar: "Bar"}, nil
	}
	server := httptest.NewServer(Handler(NoDepsInit, h))
	defer server.Close()

	u := urlx.MustParse(server.URL)
	stub := StubFromHandler(server.Client(), u, h)

	ctx := context.Background()
	result, err := stub(ctx, FooRequest{Foo: "foo"})

	if err != nil {
		t.Errorf("Stub returned an error: %v", err)
	}
	expected := FooResponse{Bar: "Bar"}
	if !reflect.DeepEqual(*result, expected) {
		t.Errorf("Expected %v, got %v", expected, *result)
	}
}

func TestHandler(t *testing.T) {
	handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		if req.Foo != "foo" {
			t.Errorf("request.Foo: want='foo' got='%s'", req.Foo)
		}
		return &FooResponse{Bar: "Bar"}, nil
	}

	server := httptest.NewServer(Handler(NoDepsInit, handler))
	defer server.Close()

	resp, err := server.Client().PostForm(server.URL, url.Values{"foo": {"foo"}})

	if err != nil {
		t.Fatalf("Request returned an error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Error unmarshaling response: %v", err)
	}

	expected := map[string]string{"Bar": "Bar"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

// Test for AsStatus
func TestAsStatus(t *testing.T) {
	err := AsStatus(codes.NotFound, errors.New("foo"))
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("AsStatus did not return a status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected code NotFound, got %v", st.Code())
	}
	if st.Message() != "foo" {
		t.Errorf("Expected message '%s', got '%s'", "foo", st.Message())
	}
}

func TestHandlerWithError(t *testing.T) {
	handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		return nil, AsStatus(codes.InvalidArgument, errors.New("foo"))
	}

	server := httptest.NewServer(Handler(NoDepsInit, handler))
	defer server.Close()

	resp, err := http.PostForm(server.URL, url.Values{"foo": {"foo"}})

	if err != nil {
		t.Errorf("Request returned an error: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	expectedBody := "Bad Request\n"
	b, _ := io.ReadAll(resp.Body)
	if string(b) != expectedBody {
		t.Errorf("Expected body '%s', got '%s'", expectedBody, string(b))
	}
}

type fakeHandler struct {
	got *http.Request
}

func (h *fakeHandler) handle(_ http.ResponseWriter, r *http.Request) {
	h.got = r
}

type fakeTransltor struct {
	got  string
	send FooRequest
}

func (t *fakeTransltor) translate(r io.ReadCloser) (FooRequest, error) {
	t.got = string(must(io.ReadAll(r)))
	return t.send, nil
}

func TestTranslate(t *testing.T) {
	h := &fakeHandler{}
	ft := &fakeTransltor{send: FooRequest{Foo: "foo"}}
	handler := Translate(ft.translate, h.handle)
	handler(nil, &http.Request{URL: must(url.Parse("http://example.com")), Body: io.NopCloser(strings.NewReader("foo"))})
	if ft.got != "foo" {
		t.Errorf("Expected ft.got 'foo', got '%s'", ft.got)
	}
	if h.got.URL.RawQuery != "foo=foo" {
		t.Errorf("Expected h.got.URL.RawQuery 'foo=foo', got '%s'", h.got.URL.RawQuery)
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
