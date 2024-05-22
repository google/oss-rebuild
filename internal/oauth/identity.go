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

package oauth

import (
	"context"
	"net/http"

	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	gapihttp "google.golang.org/api/transport/http"
)

// AuthorizedUserIDClient provides a client that attaches an id_token derived from the default credential.
func AuthorizedUserIDClient(ctx context.Context) (*http.Client, error) {
	// idtoken doesn't support user credentials.
	// https://github.com/googleapis/google-api-go-client/issues/873
	ts, err := AuthorizedUserTokenSource(ctx)
	if err != nil {
		return nil, err
	}
	ht := http.DefaultTransport.(*http.Transport).Clone()
	ht.MaxIdleConnsPerHost = 100
	gapiht, err := gapihttp.NewTransport(ctx, ht, option.WithTokenSource(ts))
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: gapiht}, nil
}

// AuthorizedUserTokenSource provides a token source for the id_token derived from the default credential.
func AuthorizedUserTokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	ts, err := google.DefaultTokenSource(ctx)
	if err != nil {
		return nil, err
	}
	return oauth2.ReuseTokenSource(nil, &idTokenSource{TokenSource: ts}), nil
}

type idTokenSource struct {
	TokenSource oauth2.TokenSource
}

func (s *idTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.TokenSource.Token()
	if err != nil {
		return nil, err
	}
	if idToken, ok := token.Extra("id_token").(string); ok {
		return &oauth2.Token{AccessToken: idToken, Expiry: token.Expiry}, nil
	}
	return nil, errors.Errorf("token did not contain an id_token")
}
