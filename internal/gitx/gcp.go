// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"context"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/oauth2/google"
)

// IsSSMURL returns true if the URL refers to a GCP Secure Source Manager repo.
func IsSSMURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(u.Hostname(), ".sourcemanager.dev")
}

// GCPBasicAuth returns an AuthMethod using a GCP access token from the default credential.
func GCPBasicAuth(ctx context.Context) (transport.AuthMethod, error) {
	ts, err := google.DefaultTokenSource(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := ts.Token()
	if err != nil {
		return nil, err
	}
	return &http.BasicAuth{
		Username: "x-access-token",
		Password: tok.AccessToken,
	}, nil
}
