// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/genai"
)

func TestGenerateTextContentWithUsage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		want     string
		wantErr  string
	}{
		{
			name:     "Text",
			response: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`,
			want:     "hello",
		},
		{
			// Search-grounded turns return this shape.
			name:     "StopWithNoContent",
			response: `{"candidates":[{"finishReason":"STOP"}]}`,
			wantErr:  "empty response content",
		},
		{
			name:     "StopWithNoParts",
			response: `{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}`,
			wantErr:  "empty response content",
		},
		{
			name:     "NoCandidates",
			response: `{"candidates":[]}`,
			wantErr:  "no candidates returned",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tr := &scriptedTransport{responses: []string{tc.response}}
			client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := GenerateTextContentWithUsage(ctx, client, "m", nil, genai.NewPartFromText("prompt"))
			switch {
			case tc.wantErr != "" && err == nil:
				t.Fatalf("GenerateTextContentWithUsage() = %q, want error %q", got, tc.wantErr)
			case tc.wantErr != "" && err.Error() != tc.wantErr:
				t.Fatalf("GenerateTextContentWithUsage() error = %q, want %q", err, tc.wantErr)
			case tc.wantErr == "" && err != nil:
				t.Fatalf("GenerateTextContentWithUsage(): %v", err)
			case tc.wantErr == "" && got != tc.want:
				t.Errorf("GenerateTextContentWithUsage() = %q, want %q", got, tc.want)
			}
		})
	}
}
