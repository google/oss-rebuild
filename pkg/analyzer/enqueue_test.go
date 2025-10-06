// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzer

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestGCSEventToTargetEvent(t *testing.T) {
	tests := []struct {
		name        string
		objectName  string
		want        *schema.TargetEvent
		wantErr     bool
		errContains string
	}{
		{
			name:       "regular package - valid",
			objectName: "npm/lodash/4.17.21/lodash-4.17.21.tgz/rebuild.intoto.jsonl",
			want: &schema.TargetEvent{
				Ecosystem: rebuild.NPM,
				Package:   "lodash",
				Version:   "4.17.21",
				Artifact:  "lodash-4.17.21.tgz",
			},
		},
		{
			name:       "scoped package - valid",
			objectName: "npm/@fortawesome/react-fontawesome/0.2.5/fortawesome-react-fontawesome-0.2.5.tgz/rebuild.intoto.jsonl",
			want: &schema.TargetEvent{
				Ecosystem: rebuild.NPM,
				Package:   "@fortawesome/react-fontawesome",
				Version:   "0.2.5",
				Artifact:  "fortawesome-react-fontawesome-0.2.5.tgz",
			},
		},
		{
			name:       "different ecosystem - maven",
			objectName: "maven/com.google.guava/31.1-jre/guava-31.1-jre.jar/rebuild.intoto.jsonl",
			want: &schema.TargetEvent{
				Ecosystem: rebuild.Maven,
				Package:   "com.google.guava",
				Version:   "31.1-jre",
				Artifact:  "guava-31.1-jre.jar",
			},
		},
		{
			name:        "regular package - too few segments",
			objectName:  "npm/lodash/4.17.21/rebuild.intoto.jsonl",
			wantErr:     true,
			errContains: "unexpected object path length",
		},
		{
			name:        "regular package - scoped package segments without @",
			objectName:  "npm/lodash/4.17.21/artifact/extra/rebuild.intoto.jsonl",
			wantErr:     true,
			errContains: "unexpected package scope for scoped object path",
		},
		{
			name:       "scoped package - too few segments parsed as regular",
			objectName: "npm/@scope/package/version/rebuild.intoto.jsonl",
			want: &schema.TargetEvent{
				Ecosystem: rebuild.NPM,
				Package:   "@scope",
				Version:   "package",
				Artifact:  "version",
			},
		},
		{
			name:        "wrong file extension",
			objectName:  "npm/lodash/4.17.21/lodash-4.17.21.tgz/wrong.file",
			wantErr:     true,
			errContains: "unexpected object name",
		},
		{
			name:        "scoped package wrong file extension",
			objectName:  "npm/@scope/package/1.0.0/artifact/wrong.file",
			wantErr:     true,
			errContains: "unexpected object name",
		},
		{
			name:        "empty object name",
			objectName:  "",
			wantErr:     true,
			errContains: "unexpected object path length",
		},
		{
			name:        "single segment",
			objectName:  "npm",
			wantErr:     true,
			errContains: "unexpected object path length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GCSEventToTargetEvent(schema.GCSObjectEvent{
				Name: tt.objectName,
			})
			if tt.wantErr {
				if err == nil {
					t.Errorf("GCSEventToTargetEvent() expected error, got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("GCSEventToTargetEvent() error = %v, want error containing %q", err, tt.errContains)
				}
				if got != nil {
					t.Errorf("GCSEventToTargetEvent() expected nil result on error, got %v", got)
				}
			} else {
				if err != nil {
					t.Errorf("GCSEventToTargetEvent() unexpected error = %v", err)
				}
				if diff := cmp.Diff(tt.want, got); diff != "" {
					t.Errorf("GCSEventToTargetEvent() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestGCSEventBodyToTargetEvent(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		want        *schema.TargetEvent
		wantErr     bool
		errContains string
	}{
		{
			name: "valid regular package",
			body: `{
				"name": "npm/lodash/4.17.21/lodash-4.17.21.tgz/rebuild.intoto.jsonl",
				"bucket": "test-rebuild-attestations"
			}`,
			want: &schema.TargetEvent{
				Ecosystem: rebuild.Ecosystem("npm"),
				Package:   "lodash",
				Version:   "4.17.21",
				Artifact:  "lodash-4.17.21.tgz",
			},
		},
		{
			name: "valid scoped package event",
			body: `{
				"name": "npm/@fortawesome/react-fontawesome/0.2.5/fortawesome-react-fontawesome-0.2.5.tgz/rebuild.intoto.jsonl",
				"bucket": "test-rebuild-attestations",
				"generation": "1756248350768017",
				"timeCreated": "2025-08-26T22:45:50.776575Z",
				"updated": "2025-08-26T22:45:50.776575Z",
				"size": "2048"
			}`,
			want: &schema.TargetEvent{
				Ecosystem: rebuild.Ecosystem("npm"),
				Package:   "@fortawesome/react-fontawesome",
				Version:   "0.2.5",
				Artifact:  "fortawesome-react-fontawesome-0.2.5.tgz",
			},
		},
		{
			name:        "invalid JSON",
			body:        `{"name": "invalid json"`,
			wantErr:     true,
			errContains: "decoding event",
		},
		{
			name: "valid JSON but delegates parsing error to underlying function",
			body: `{
				"name": "npm/package/rebuild.intoto.jsonl"
			}`,
			wantErr:     true,
			errContains: "unexpected object path length",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GCSEventBodyToTargetEvent(io.NopCloser(strings.NewReader(tt.body)))
			if tt.wantErr {
				if err == nil {
					t.Errorf("GCSEventBodyToTargetEvent() expected error, got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("GCSEventBodyToTargetEvent() error = %v, want error containing %q", err, tt.errContains)
				}
				if got != nil {
					t.Errorf("GCSEventBodyToTargetEvent() expected nil result on error, got %v", got)
				}
			} else {
				if err != nil {
					t.Errorf("GCSEventBodyToTargetEvent() unexpected error = %v", err)
				}
				if diff := cmp.Diff(tt.want, got); diff != "" {
					t.Errorf("GCSEventBodyToTargetEvent() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

type mockReadCloser struct {
	*bytes.Reader
	closed bool
}

func (m *mockReadCloser) Close() error {
	m.closed = true
	return nil
}

// TestGCSEventBodyToTargetEvent_BodyClosure tests that the body is properly closed
func TestGCSEventBodyToTargetEvent_BodyClosure(t *testing.T) {
	validJSON := `{"name": "npm/test/1.0.0/test-1.0.0.tgz/rebuild.intoto.jsonl"}`
	mock := &mockReadCloser{
		Reader: bytes.NewReader([]byte(validJSON)),
		closed: false,
	}
	result, err := GCSEventBodyToTargetEvent(mock)
	if err != nil {
		t.Errorf("GCSEventBodyToTargetEvent() unexpected error = %v", err)
	}
	if result == nil {
		t.Errorf("GCSEventBodyToTargetEvent() expected result, got nil")
	}
	if !mock.closed {
		t.Errorf("Body should be closed after processing")
	}
}
