// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpxtest

import (
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type Call struct {
	Method   string
	URL      string
	Response *http.Response
	Error    error
}

type MockClient struct {
	Calls             []Call
	URLValidator      func(expected, actual string)
	SkipURLValidation bool
	callCount         int
}

func (m *MockClient) Do(req *http.Request) (*http.Response, error) {
	if m.callCount >= len(m.Calls) {
		panic("unexpected request")
	}
	call := m.Calls[m.callCount]
	m.callCount++

	if !m.SkipURLValidation && (m.URLValidator == nil) {
		panic("URL validation requested but not configured")
	} else if m.SkipURLValidation && (m.URLValidator != nil) {
		panic("URL validation disabled but configured")
	}
	if m.URLValidator != nil {
		if call.Method != "" {
			m.URLValidator(call.Method+" "+call.URL, req.Method+" "+req.URL.String())
		} else {
			m.URLValidator(call.URL, req.URL.String())
		}
	}

	return call.Response, call.Error
}

func (m *MockClient) CallCount() int {
	return m.callCount
}

func NewURLValidator(t *testing.T) func(string, string) {
	return func(expected, actual string) {
		t.Helper()
		if diff := cmp.Diff(expected, actual); diff != "" {
			t.Fatalf("URL mismatch (-want +got):\n%s", diff)
		}
	}
}
