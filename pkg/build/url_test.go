// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"testing"
)

func TestGSToHTTP(t *testing.T) {
	tests := []struct {
		name     string
		gsURL    string
		wantHTTP string
		wantErr  bool
	}{
		{
			name:     "basic gs URL",
			gsURL:    "gs://bucket/object",
			wantHTTP: "https://storage.googleapis.com/bucket/object",
			wantErr:  false,
		},
		{
			name:    "non-gs URL",
			gsURL:   "http://example.com/file",
			wantErr: true,
		},
		{
			name:     "invalid URL",
			gsURL:    "gs://",
			wantHTTP: "https://storage.googleapis.com//",
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpURL, err := gsToHTTP(tt.gsURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("GSToHTTP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if httpURL != tt.wantHTTP {
				t.Errorf("GSToHTTP() httpURL = %v, want %v", httpURL, tt.wantHTTP)
			}
		})
	}
}

func TestNeedsAuth(t *testing.T) {
	authPrefixes := []string{"gs://", "https://private.example.com/"}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "gs URL needs auth",
			url:  "gs://bucket/object",
			want: true,
		},
		{
			name: "private URL needs auth",
			url:  "https://private.example.com/file",
			want: true,
		},
		{
			name: "public URL no auth",
			url:  "https://public.example.com/file",
			want: false,
		},
		{
			name: "http URL no auth",
			url:  "http://example.com/file",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsAuth(tt.url, authPrefixes); got != tt.want {
				t.Errorf("NeedsAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertURLForRuntime(t *testing.T) {
	tests := []struct {
		name        string
		originalURL string
		wantURL     string
		wantErr     bool
	}{
		{
			name:        "gs URL converted",
			originalURL: "gs://bucket/object",
			wantURL:     "https://storage.googleapis.com/bucket/object",
			wantErr:     false,
		},
		{
			name:        "http URL unchanged",
			originalURL: "http://example.com/file",
			wantURL:     "http://example.com/file",
			wantErr:     false,
		},
		{
			name:        "https URL unchanged",
			originalURL: "https://example.com/file",
			wantURL:     "https://example.com/file",
			wantErr:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, err := ConvertURLForRuntime(tt.originalURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertURLForRuntime() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotURL != tt.wantURL {
				t.Errorf("ConvertURLForRuntime() gotURL = %v, want %v", gotURL, tt.wantURL)
			}
		})
	}
}
