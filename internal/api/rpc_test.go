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
	"time"

	"github.com/google/oss-rebuild/internal/urlx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
			t.Errorf("request method = %s, want POST", r.Method)
		}
		if form := r.Form.Encode(); form != "foo=foo" {
			t.Errorf("form = %q, want %q", form, "foo=foo")
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
		t.Errorf("result = %v, want %v", result, expected)
	}
}

func TestStubFromHandler(t *testing.T) {
	h := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		if req.Foo != "foo" {
			t.Errorf("request.Foo = %q, want %q", req.Foo, "foo")
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
		t.Errorf("result = %v, want %v", *result, expected)
	}
}

func TestHandler(t *testing.T) {
	handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		if req.Foo != "foo" {
			t.Errorf("request.Foo = %q, want %q", req.Foo, "foo")
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
		t.Errorf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Error unmarshaling response: %v", err)
	}

	expected := map[string]string{"Bar": "Bar"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("result = %v, want %v", result, expected)
	}
}

// Test for AsStatus with comprehensive table-driven tests
func TestAsStatus(t *testing.T) {
	tests := []struct {
		name            string
		code            codes.Code
		err             error
		details         []proto.Message
		expectCode      codes.Code
		expectMessage   string
		expectDetails   int
		expectRetryInfo *time.Duration
	}{
		{
			name:          "simple error without details",
			code:          codes.NotFound,
			err:           errors.New("not found"),
			expectCode:    codes.NotFound,
			expectMessage: "not found",
			expectDetails: 0,
		},
		{
			name:            "error with retry info",
			code:            codes.Unavailable,
			err:             errors.New("service unavailable"),
			details:         []proto.Message{RetryAfter(30 * time.Second)},
			expectCode:      codes.Unavailable,
			expectMessage:   "service unavailable",
			expectDetails:   1,
			expectRetryInfo: timePtr(30 * time.Second),
		},
		{
			name: "error with multiple details",
			code: codes.InvalidArgument,
			err:  errors.New("bad request"),
			details: []proto.Message{
				RetryAfter(60 * time.Second),
				&errdetails.ErrorInfo{Reason: "INVALID_FORMAT"},
			},
			expectCode:      codes.InvalidArgument,
			expectMessage:   "bad request",
			expectDetails:   2,
			expectRetryInfo: timePtr(60 * time.Second),
		},
		{
			name:            "zero duration retry info",
			code:            codes.Unavailable,
			err:             errors.New("unavailable"),
			details:         []proto.Message{RetryAfter(0)},
			expectCode:      codes.Unavailable,
			expectMessage:   "unavailable",
			expectDetails:   1,
			expectRetryInfo: timePtr(0),
		},
		{
			name:            "large duration retry info",
			code:            codes.Unavailable,
			err:             errors.New("unavailable"),
			details:         []proto.Message{RetryAfter(24 * time.Hour)},
			expectCode:      codes.Unavailable,
			expectMessage:   "unavailable",
			expectDetails:   1,
			expectRetryInfo: timePtr(24 * time.Hour),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := AsStatus(tc.code, tc.err, tc.details...)
			st, ok := status.FromError(err)
			if !ok {
				t.Fatal("AsStatus did not return a status error")
			}
			if st.Code() != tc.expectCode {
				t.Errorf("code = %v, want %v", st.Code(), tc.expectCode)
			}
			if st.Message() != tc.expectMessage {
				t.Errorf("message = %q, want %q", st.Message(), tc.expectMessage)
			}
			details := st.Details()
			if len(details) != tc.expectDetails {
				t.Errorf("details length = %d, want %d", len(details), tc.expectDetails)
			}
			// Check retry info if expected
			if tc.expectRetryInfo != nil {
				var foundRetryInfo *errdetails.RetryInfo
				for _, detail := range details {
					if ri, ok := detail.(*errdetails.RetryInfo); ok {
						foundRetryInfo = ri
						break
					}
				}
				if foundRetryInfo == nil {
					t.Error("Expected RetryInfo detail but not found")
				} else if foundRetryInfo.RetryDelay == nil {
					t.Error("RetryInfo has nil RetryDelay")
				} else {
					actualDuration := foundRetryInfo.RetryDelay.AsDuration()
					if actualDuration != *tc.expectRetryInfo {
						t.Errorf("retry duration = %v, want %v", actualDuration, *tc.expectRetryInfo)
					}
				}
			}
		})
	}
}

func TestHandlerWithError(t *testing.T) {
	tests := []struct {
		name           string
		handlerErr     error
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "normal error",
			handlerErr:     errors.New("foo"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "foo\n",
		},
		{
			name:           "grpc error",
			handlerErr:     AsStatus(codes.InvalidArgument, errors.New("foo")),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "foo\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
				return nil, tc.handlerErr
			}
			server := httptest.NewServer(Handler(NoDepsInit, handler))
			defer server.Close()
			resp, err := http.PostForm(server.URL, url.Values{"foo": {"foo"}})
			if err != nil {
				t.Errorf("Request returned an error: %v", err)
			}
			if resp.StatusCode != tc.expectedStatus {
				t.Errorf("Expected status code %d (%s), got %d (%s)", tc.expectedStatus, http.StatusText(tc.expectedStatus), resp.StatusCode, http.StatusText(resp.StatusCode))
			}
			b, _ := io.ReadAll(resp.Body)
			if string(b) != tc.expectedBody {
				t.Errorf("Expected body '%s', got '%s'", tc.expectedBody, string(b))
			}
		})
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
		t.Errorf("ft.got = %q, want %q", ft.got, "foo")
	}
	if h.got.URL.RawQuery != "foo=foo" {
		t.Errorf("h.got.URL.RawQuery = %q, want %q", h.got.URL.RawQuery, "foo=foo")
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

// timePtr returns a pointer to a time.Duration
func timePtr(d time.Duration) *time.Duration {
	return &d
}

// TestRetryAfter tests the RetryAfter helper function
func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
	}{
		{"zero duration", 0},
		{"30 seconds", 30 * time.Second},
		{"5 minutes", 5 * time.Minute},
		{"2 hours", 2 * time.Hour},
		{"24 hours", 24 * time.Hour},
		{"negative duration", -30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			retryInfo := RetryAfter(tc.duration)
			// Check that it returns the correct type
			ri, ok := retryInfo.(*errdetails.RetryInfo)
			if !ok {
				t.Fatalf("RetryAfter returned %T, expected *errdetails.RetryInfo", retryInfo)
			}
			// Check that RetryDelay is set
			if ri.RetryDelay == nil {
				t.Fatal("RetryInfo.RetryDelay is nil")
			}
			// Check that the duration is correct
			actualDuration := ri.RetryDelay.AsDuration()
			if actualDuration != tc.duration {
				t.Errorf("duration = %v, want %v", actualDuration, tc.duration)
			}
		})
	}
}

// TestStub_RetryAfter tests Stub's handling of HTTP retry-after headers
func TestStub_RetryAfter(t *testing.T) {
	tests := []struct {
		name              string
		httpStatus        int
		retryAfterHeader  string
		expectedRetryInfo *time.Duration
		expectedGRPCCode  codes.Code
	}{
		{
			name:              "503 with valid retry-after",
			httpStatus:        http.StatusServiceUnavailable,
			retryAfterHeader:  "30",
			expectedRetryInfo: timePtr(30 * time.Second),
			expectedGRPCCode:  codes.Unavailable,
		},
		{
			name:              "503 with zero retry-after",
			httpStatus:        http.StatusServiceUnavailable,
			retryAfterHeader:  "0",
			expectedRetryInfo: nil, // zero duration should not create retry info
			expectedGRPCCode:  codes.Unavailable,
		},
		{
			name:              "503 with invalid retry-after",
			httpStatus:        http.StatusServiceUnavailable,
			retryAfterHeader:  "invalid",
			expectedRetryInfo: nil,
			expectedGRPCCode:  codes.Unavailable,
		},
		{
			name:              "503 with negative retry-after",
			httpStatus:        http.StatusServiceUnavailable,
			retryAfterHeader:  "-30",
			expectedRetryInfo: nil, // negative duration should not create retry info
			expectedGRPCCode:  codes.Unavailable,
		},
		{
			name:              "503 without retry-after header",
			httpStatus:        http.StatusServiceUnavailable,
			retryAfterHeader:  "",
			expectedRetryInfo: nil,
			expectedGRPCCode:  codes.Unavailable,
		},
		{
			name:             "429 too many requests",
			httpStatus:       http.StatusTooManyRequests,
			expectedGRPCCode: codes.ResourceExhausted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfterHeader != "" {
					w.Header().Set("Retry-After", tc.retryAfterHeader)
				}
				w.WriteHeader(tc.httpStatus)
			}))
			defer server.Close()
			u := urlx.MustParse(server.URL)
			stub := Stub[FooRequest, FooResponse](server.Client(), u)
			ctx := context.Background()
			_, err := stub(ctx, FooRequest{Foo: "foo"})
			if err == nil {
				t.Fatal("Expected error but got none")
			}
			// Check if it's a gRPC status error with the right code
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got %T: %v", err, err)
			}
			if st.Code() != tc.expectedGRPCCode {
				t.Errorf("gRPC code = %v, want %v", st.Code(), tc.expectedGRPCCode)
			}
			// Check retry info if expected
			if tc.expectedRetryInfo != nil {
				details := st.Details()
				var foundRetryInfo *errdetails.RetryInfo
				for _, detail := range details {
					if ri, ok := detail.(*errdetails.RetryInfo); ok {
						foundRetryInfo = ri
						break
					}
				}
				if foundRetryInfo == nil {
					t.Error("Expected RetryInfo detail but not found")
				} else if foundRetryInfo.RetryDelay == nil {
					t.Error("RetryInfo has nil RetryDelay")
				} else {
					actualDuration := foundRetryInfo.RetryDelay.AsDuration()
					if actualDuration != *tc.expectedRetryInfo {
						t.Errorf("retry duration = %v, want %v", actualDuration, *tc.expectedRetryInfo)
					}
				}
			} else {
				// Should not have retry info
				details := st.Details()
				for _, detail := range details {
					if _, ok := detail.(*errdetails.RetryInfo); ok {
						t.Error("Unexpected RetryInfo detail found")
					}
				}
			}
		})
	}
}

// TestHandler_Errors tests Handler's setting of HTTP errors and the retry-after header
func TestHandler_Errors(t *testing.T) {
	tests := []struct {
		name                     string
		handlerError             error
		expectedHTTPStatus       int
		expectedRetryAfterHeader string
		expectedResponseBody     string
	}{
		{
			name:                     "normal error",
			handlerError:             AsStatus(codes.NotFound, errors.New("not found")),
			expectedHTTPStatus:       http.StatusNotFound,
			expectedRetryAfterHeader: "",
			expectedResponseBody:     "not found\n",
		},
		{
			name:                     "normal error (non-grpc)",
			handlerError:             errors.New("regular error"),
			expectedHTTPStatus:       http.StatusInternalServerError,
			expectedRetryAfterHeader: "",
			expectedResponseBody:     "regular error\n",
		},
		{
			name: "unavailable with retry info",
			handlerError: AsStatus(codes.Unavailable,
				errors.New("service unavailable"),
				RetryAfter(45*time.Second)),
			expectedHTTPStatus:       http.StatusServiceUnavailable,
			expectedRetryAfterHeader: "45",
			expectedResponseBody:     "service unavailable\n",
		},
		{
			name:                     "unavailable without retry info",
			handlerError:             AsStatus(codes.Unavailable, errors.New("unavailable")),
			expectedHTTPStatus:       http.StatusServiceUnavailable,
			expectedRetryAfterHeader: "", // no header should be set
			expectedResponseBody:     "unavailable\n",
		},
		{
			name: "retry info with zero duration",
			handlerError: AsStatus(codes.Unavailable,
				errors.New("unavailable"),
				RetryAfter(0)),
			expectedHTTPStatus:       http.StatusServiceUnavailable,
			expectedRetryAfterHeader: "", // zero duration should not set header
			expectedResponseBody:     "unavailable\n",
		},
		{
			name: "multiple details with retry info",
			handlerError: AsStatus(codes.ResourceExhausted,
				errors.New("resource exhausted"),
				RetryAfter(120*time.Second),
				&errdetails.ErrorInfo{Reason: "QUOTA_ERROR"}),
			expectedHTTPStatus:       http.StatusTooManyRequests,
			expectedRetryAfterHeader: "120",
			expectedResponseBody:     "resource exhausted\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
				return nil, tc.handlerError
			}
			server := httptest.NewServer(Handler(NoDepsInit, handler))
			defer server.Close()
			resp, err := http.PostForm(server.URL, url.Values{"foo": {"test"}})
			if err != nil {
				t.Fatalf("Request returned an error: %v", err)
			}
			defer resp.Body.Close()
			// Check HTTP status
			if resp.StatusCode != tc.expectedHTTPStatus {
				t.Errorf("status code = %d (%s), want %d (%s)",
					resp.StatusCode, http.StatusText(resp.StatusCode),
					tc.expectedHTTPStatus, http.StatusText(tc.expectedHTTPStatus))
			}
			// Check Retry-After header
			retryAfterHeader := resp.Header.Get("Retry-After")
			if retryAfterHeader != tc.expectedRetryAfterHeader {
				t.Errorf("Retry-After header = %q, want %q",
					retryAfterHeader, tc.expectedRetryAfterHeader)
			}
			// Check response body
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Error reading response body: %v", err)
			}
			if string(body) != tc.expectedResponseBody {
				t.Errorf("response body = %q, want %q",
					string(body), tc.expectedResponseBody)
			}
		})
	}
}

// TestStubHandlerRoundTrip_RetryAfter tests full round-trip retry-after functionality
func TestStubHandlerRoundTrip_RetryAfter(t *testing.T) {
	tests := []struct {
		name                  string
		handlerError          error
		expectedRetryDuration *time.Duration
		expectedGRPCCode      codes.Code
	}{
		{
			name: "round-trip with 30 second retry",
			handlerError: AsStatus(codes.Unavailable,
				errors.New("service unavailable"),
				RetryAfter(30*time.Second)),
			expectedRetryDuration: timePtr(30 * time.Second),
			expectedGRPCCode:      codes.Unavailable,
		},
		{
			name: "round-trip with 5 minute retry",
			handlerError: AsStatus(codes.Unavailable,
				errors.New("temporarily unavailable"),
				RetryAfter(5*time.Minute)),
			expectedRetryDuration: timePtr(5 * time.Minute),
			expectedGRPCCode:      codes.Unavailable,
		},
		{
			name: "round-trip with zero retry (should not preserve)",
			handlerError: AsStatus(codes.Unavailable,
				errors.New("unavailable"),
				RetryAfter(0)),
			expectedRetryDuration: nil, // zero should not create retry header or be parsed
			expectedGRPCCode:      codes.Unavailable,
		},
		{
			name:                  "round-trip without retry info",
			handlerError:          AsStatus(codes.Unavailable, errors.New("unavailable")),
			expectedRetryDuration: nil,
			expectedGRPCCode:      codes.Unavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Step 1: Create a handler that returns the error
			handler := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
				return nil, tc.handlerError
			}
			// Step 2: Create HTTP server with the handler
			server := httptest.NewServer(Handler(NoDepsInit, handler))
			defer server.Close()
			// Step 3: Create a stub that talks to the handler
			u := urlx.MustParse(server.URL)
			stub := Stub[FooRequest, FooResponse](server.Client(), u)
			// Step 4: Call the stub and verify round-trip behavior
			result, err := stub(t.Context(), FooRequest{Foo: "test"})
			// Should always get an error for these test cases
			if err == nil {
				t.Fatal("Expected error but got none")
			}
			// Result should be nil for error cases
			if result != nil {
				t.Error("Expected nil result for error case")
			}
			// Check that it's a gRPC status error with the right code
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got %T: %v", err, err)
			}
			if st.Code() != tc.expectedGRPCCode {
				t.Errorf("gRPC code = %v, want %v", st.Code(), tc.expectedGRPCCode)
			}
			// Check retry info preservation through round-trip
			details := st.Details()
			var foundRetryInfo *errdetails.RetryInfo
			for _, detail := range details {
				if ri, ok := detail.(*errdetails.RetryInfo); ok {
					foundRetryInfo = ri
					break
				}
			}
			if tc.expectedRetryDuration != nil {
				if foundRetryInfo == nil {
					t.Error("Expected RetryInfo detail but not found after round-trip")
				} else if foundRetryInfo.RetryDelay == nil {
					t.Error("RetryInfo has nil RetryDelay after round-trip")
				} else {
					actualDuration := foundRetryInfo.RetryDelay.AsDuration()
					if actualDuration != *tc.expectedRetryDuration {
						t.Errorf("retry duration after round-trip = %v, want %v",
							actualDuration, *tc.expectedRetryDuration)
					}
				}
			} else {
				if foundRetryInfo != nil {
					t.Errorf("Unexpected RetryInfo found after round-trip: %v",
						foundRetryInfo.RetryDelay.AsDuration())
				}
			}
		})
	}
}
