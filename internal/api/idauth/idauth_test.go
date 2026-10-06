// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package idauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg/errors"
)

type fakeValidator struct {
	accept string // the one token value that passes
	email  string
}

func (f fakeValidator) Validate(_ context.Context, token string) (string, error) {
	if token == f.accept {
		return f.email, nil
	}
	return "", errors.New("not authorized")
}

func wrap(t *testing.T, v Validator) *httptest.Server {
	t.Helper()
	mw := Middleware(v)
	h := mw(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusNoContent) // distinct success signal
	}))
	return httptest.NewServer(h)
}

func TestMiddleware_MissingHeader(t *testing.T) {
	srv := wrap(t, fakeValidator{accept: "good", email: "broker@example.com"})
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", resp.StatusCode)
	}
}

func TestMiddleware_NonBearer(t *testing.T) {
	srv := wrap(t, fakeValidator{accept: "good"})
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Basic Z29vZA==")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", resp.StatusCode)
	}
}

func TestMiddleware_BadToken(t *testing.T) {
	srv := wrap(t, fakeValidator{accept: "good"})
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer not-the-good-one")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", resp.StatusCode)
	}
}

func TestMiddleware_GoodToken(t *testing.T) {
	srv := wrap(t, fakeValidator{accept: "good", email: "broker@example.com"})
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d; want 204 (handler reached)", resp.StatusCode)
	}
}

func TestBearer_WhitespaceAndCase(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":    "abc",
		"Bearer  abc":   "abc",
		"Bearer abc ":   "abc",
		"":              "",
		"bearer abc":    "", // case-sensitive on purpose
		"Bearer":        "",
		"NotBearer abc": "",
	}
	for header, want := range cases {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if got := bearer(req); got != want {
			t.Errorf("bearer(%q) = %q; want %q", header, got, want)
		}
	}
}
