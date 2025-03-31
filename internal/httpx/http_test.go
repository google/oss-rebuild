// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"io"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/pkg/errors"
)

func TestCachedClient(t *testing.T) {
	for _, tc := range []struct {
		name              string
		callsToCache      []httpxtest.Call
		callsToBaseClient []httpxtest.Call
	}{
		{
			name: "single request",
			callsToCache: []httpxtest.Call{
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
			callsToBaseClient: []httpxtest.Call{
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
		},
		{
			name: "cached request",
			callsToCache: []httpxtest.Call{
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
			callsToBaseClient: []httpxtest.Call{ // Only one call to base client
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
		},
		{
			name: "don't cache 500",
			callsToCache: []httpxtest.Call{
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "500 Internal Server Error",
						StatusCode: http.StatusInternalServerError,
						Body:       httpxtest.Body(""),
					},
				},
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
			callsToBaseClient: []httpxtest.Call{ // Two calls to base client, second is success
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "500 Internal Server Error",
						StatusCode: http.StatusInternalServerError,
						Body:       httpxtest.Body(""),
					},
				},
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Body:       httpxtest.Body("body"),
					},
				},
			},
		},
		{
			name: "do cache 404",
			callsToCache: []httpxtest.Call{
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "404 Not Found",
						StatusCode: http.StatusNotFound,
						Body:       httpxtest.Body(""),
					},
				},
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "404 Not Found",
						StatusCode: http.StatusNotFound,
						Body:       httpxtest.Body(""),
					},
				},
			},
			callsToBaseClient: []httpxtest.Call{ // Only one call, 404 errors are cached.
				{
					Method: "GET",
					URL:    "http://example.com",
					Response: &http.Response{
						Status:     "404 Not Found",
						StatusCode: http.StatusNotFound,
						Body:       httpxtest.Body(""),
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			basic := &httpxtest.MockClient{
				Calls:             tc.callsToBaseClient,
				SkipURLValidation: true,
			}
			cached := NewCachedClient(basic, &cache.CoalescingMemoryCache{})
			for i, call := range tc.callsToCache {
				resp, err := cached.Do(call.Request())
				if (err != nil) != (call.Error != nil) {
					t.Fatalf("(call %d) expected error %v, got %v", i, call.Error, err)
				}
				if err != nil && call.Error != nil && err.Error() != call.Error.Error() {
					t.Fatalf("(call %d) errors mismatch want %v, got %v", i, call.Error, err)
				}
				if (resp != nil) != (call.Response != nil) {
					t.Fatalf("(call %d) response mismatch want %v, got %v", i, call.Response, resp)
				}
				if resp == nil || call.Response == nil {
					return
				}
				if resp.Status != call.Response.Status {
					t.Fatalf("(call %d) Status mismatch want %v, got %v", i, call.Response.Status, resp.Status)
				}
				if resp.StatusCode != call.Response.StatusCode {
					t.Fatalf("(call %d) StatusCode mismatch want %v, got %v", i, call.Response.StatusCode, resp.StatusCode)
				}
				respBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(errors.Wrap(err, "reading response body"))
				}
				expectedBytes, err := io.ReadAll(call.Response.Body)
				if err != nil {
					t.Fatal(errors.Wrap(err, "reading expected response body"))
				}
				if diff := cmp.Diff(string(respBytes), string(expectedBytes)); diff != "" {
					t.Fatalf("(call %d) response body mismatch (-want +got):\n%s", i, diff)
				}
			}
		})
	}

}
