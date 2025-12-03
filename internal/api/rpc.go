// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/url"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	actapi "github.com/google/oss-rebuild/pkg/act/api"
)

// Re-exports for backwards compatibility
type (
	Message      = act.Input
	Dependencies = act.Deps
	NoDeps       = act.NoDeps
	NoReturn     = act.NoOutput

	InitT[D act.Deps]                        = actapi.InitDeps[D]
	HandlerT[I act.Input, O any, D act.Deps] = actapi.HandlerFunc[I, O, D]
	StubT[I act.Input, O any]                = actapi.StubFunc[I, O]
	Translator[O act.Input]                  = actapi.Translator[O]
)

var (
	NoDepsInit = act.NoDepsInit

	ErrNotOK       = actapi.ErrNotOK
	ErrExhausted   = actapi.ErrExhausted
	ErrUnavailable = actapi.ErrUnavailable

	AsStatus   = actapi.AsStatus
	RetryAfter = actapi.RetryAfter
)

// Generic functions re-exports (must be defined as functions, not variables)

func Stub[I act.Input, O any](client httpx.BasicClient, u *url.URL) StubT[I, O] {
	return actapi.Stub[I, O](client, u)
}

func StubFromHandler[I act.Input, O any, D act.Deps](client httpx.BasicClient, u *url.URL, handler HandlerT[I, O, D]) StubT[I, O] {
	return actapi.StubFromHandler(client, u, handler)
}

func Handler[I act.Input, O any, D act.Deps](initDeps InitT[D], handler HandlerT[I, O, D]) http.HandlerFunc {
	return actapi.Handler(initDeps, handler)
}

func Translate[O act.Input](t Translator[O], h http.HandlerFunc) http.HandlerFunc {
	return actapi.Translate(t, h)
}
