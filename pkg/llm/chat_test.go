// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// scriptedTransport answers each generateContent request with the next
// canned response and keeps the request bodies.
type scriptedTransport struct {
	responses []string
	requests  [][]byte
}

func (t *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.requests = append(t.requests, body)
	if len(t.requests) > len(t.responses) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"no more responses"}}`))}, nil
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.responses[len(t.requests)-1])),
	}, nil
}

func TestSendMessageKeepsHistoryIntactAcrossToolTurns(t *testing.T) {
	ctx := context.Background()
	tr := &scriptedTransport{responses: []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"probe","args":{"n":1}}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"probe","args":{"n":2}}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`,
	}}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	probe := &FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{Name: "probe", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"n": {Type: genai.TypeInteger}}}},
		Function: func(args map[string]any) genai.FunctionResponse {
			return genai.FunctionResponse{Name: "probe", Response: map[string]any{"echo": args["n"]}}
		},
	}
	chat, err := NewChat(ctx, client, "m", nil, &ChatOpts{Tools: []*FunctionDefinition{probe}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.SendMessage(ctx, genai.NewPartFromText("the task")); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	h := chat.History()
	if len(h) != 6 {
		t.Fatalf("history has %d contents, want 6 (prompt, call, result, call, result, answer)", len(h))
	}
	if got := h[0].Parts[0].Text; got != "the task" {
		t.Errorf("history[0] = %q, want the prompt", got)
	}
	for i, want := range map[int]float64{2: 1, 4: 2} {
		if fr := h[i].Parts[0].FunctionResponse; fr == nil || fr.Response["echo"] != want {
			t.Errorf("history[%d] = %+v, want the result of probe %v", i, h[i].Parts[0], want)
		}
	}
	// What the model was sent on the last turn must still open with the task.
	if last := tr.requests[len(tr.requests)-1]; !bytes.Contains(last, []byte("the task")) || bytes.Count(last, []byte(`"echo"`)) != 2 {
		t.Errorf("last request lost the prompt or a tool result:\n%s", last)
	}
}

func TestFinalTurnNudgePrecedesToolResults(t *testing.T) {
	ctx := context.Background()
	tr := &scriptedTransport{responses: []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"probe","args":{"n":1}}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"probe","args":{"n":2}}}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`,
	}}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	probe := &FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{Name: "probe", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"n": {Type: genai.TypeInteger}}}},
		Function: func(args map[string]any) genai.FunctionResponse {
			return genai.FunctionResponse{Name: "probe", Response: map[string]any{"echo": args["n"]}}
		},
	}
	// Three iterations: the nudge rides with the results of the second
	// response, in the third and last send.
	chat, err := NewChat(ctx, client, "m", nil, &ChatOpts{Tools: []*FunctionDefinition{probe}, MaxToolIterations: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.SendMessage(ctx, genai.NewPartFromText("the task")); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	type request struct {
		Contents []struct {
			Role  string
			Parts []struct {
				Text             string
				FunctionResponse *struct{ Name string }
			}
		}
	}
	lastTurn := func(body []byte) (string, int, bool, bool) {
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		c := req.Contents[len(req.Contents)-1]
		return c.Role, len(c.Parts), strings.Contains(c.Parts[0].Text, "final turn"), c.Parts[len(c.Parts)-1].FunctionResponse != nil
	}
	if role, n, nudged, result := lastTurn(tr.requests[2]); role != "user" || n != 2 || !nudged || !result {
		t.Errorf("last send = role %s, %d parts, nudge first %v, result last %v; want the nudge ahead of the tool result", role, n, nudged, result)
	}
	if _, n, nudged, _ := lastTurn(tr.requests[1]); n != 1 || nudged {
		t.Errorf("second send carried %d parts (nudged %v), want the tool result alone", n, nudged)
	}
}
