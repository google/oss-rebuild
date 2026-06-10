// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package idauth provides HTTP middleware that requires a Google-signed
// ID token on inbound requests. Used on the scratch worker to
// gate /exec/start and /stat to the broker's service account.
package idauth

import (
	"context"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
)

// Validator verifies an inbound bearer token. Implementations return the
// caller's email on success.
type Validator interface {
	Validate(ctx context.Context, token string) (email string, err error)
}

// googleValidator verifies a Google-signed ID token against an expected
// audience and asserts the email claim matches the configured caller SA.
type googleValidator struct {
	audience      string
	expectedEmail string
}

// NewGoogleValidator returns a Validator that requires the token to be
// signed by Google, have audience equal to audience, and carry an email
// claim equal to expectedEmail.
func NewGoogleValidator(expectedEmail, audience string) Validator {
	return &googleValidator{expectedEmail: expectedEmail, audience: audience}
}

func (g *googleValidator) Validate(ctx context.Context, token string) (string, error) {
	payload, err := idtoken.Validate(ctx, token, g.audience)
	if err != nil {
		return "", errors.Wrap(err, "idtoken validate")
	}
	email, _ := payload.Claims["email"].(string)
	if email != g.expectedEmail {
		return email, errors.Errorf("email %q does not match expected %q", email, g.expectedEmail)
	}
	return email, nil
}

// Middleware returns an HTTP middleware that gates next behind a valid
// bearer token. Failures emit 401 with a short text body.
func Middleware(v Validator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			tok := bearer(r)
			if tok == "" {
				http.Error(rw, "missing bearer token", http.StatusUnauthorized)
				return
			}
			if _, err := v.Validate(r.Context(), tok); err != nil {
				http.Error(rw, "invalid token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(rw, r)
		})
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}
