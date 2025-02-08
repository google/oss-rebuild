// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

// The file token.json stores the user's access and refresh tokens, and is
// created automatically when the authorization flow completes for the first
// time.
const tokFile = "/tmp/token.json"

// GetOauthClient retrieves a token, saves the token, then returns the generated client.
func GetOauthClient(ctx context.Context, config *oauth2.Config) (*http.Client, error) {
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok, err = promptForWebToken(ctx, config)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to fetch token")
		}
		if err := saveTokenToFile(tokFile, tok); err != nil {
			return nil, err
		}
	}
	return config.Client(ctx, tok), nil
}

// promptForWebToken starts a web oauth flow and prompts the user for the returned token.
func promptForWebToken(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, errors.Wrapf(err, "Unable to read authorization code")
	}

	tok, err := config.Exchange(ctx, authCode)
	if err != nil {
		return nil, errors.Wrapf(err, "Unable to retrieve token from web")
	}
	return tok, nil
}

// tokenFromFile retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// saveTokenToFile stores an oauth token to a file path.
func saveTokenToFile(path string, token *oauth2.Token) error {
	log.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return errors.Wrapf(err, "Unable to cache oauth token")
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
	return nil
}
