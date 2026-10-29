// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcsx/gcsxtest"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

// TestClassifyPurposes classifies a transcript: the model sees the shell calls in
// order with their results and names the purposes of each, in Purposes order
// with the primary one included, check_artifact is the comparison, and a call
// the model leaves out stays unclassified.
func TestClassifyPurposes(t *testing.T) {
	const bucket, sid = "sessions", "sess-1"
	model := func(parts ...string) string { return `{"role":"model","parts":[` + strings.Join(parts, ",") + `]}` }
	user := func(parts ...string) string { return `{"role":"user","parts":[` + strings.Join(parts, ",") + `]}` }
	call := func(name, args string) string { return `{"functionCall":{"name":"` + name + `","args":` + args + `}}` }
	resp := func(name, r string) string {
		return `{"functionResponse":{"name":"` + name + `","response":` + r + `}}`
	}
	// The second exchange's stamp sorts before the first's as a string.
	objects := map[string]string{
		bucket + "/" + sid + "/messages/1/0-inference-proposal.json":           `{}`,
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:08Z-1-model.json":   model(call("read_logs_end", `{}`), call("run_in_container", `{"command":"npm ci"}`)),
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:08Z-2-user.json":    user(resp("read_logs_end", `{"logs":"npm ERR!"}`), resp("run_in_container", `{"output":"\nadded 12 packages\n","exit_code":0}`)),
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:08.5Z-3-model.json": model(call("run_in_container", `{"command":"npm pack"}`), call("check_artifact", `{"path":"/src/p.tgz"}`)),
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:08.5Z-4-user.json":  user(resp("run_in_container", `{"output":"p-1.0.0.tgz","exit_code":1}`), resp("check_artifact", `{"result":"MATCH","match":true}`)),
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:09Z-5-model.json":   model(call("run_on_host", `{"command":"ls builds"}`)),
		bucket + "/" + sid + "/messages/2/2026-10-20T00:25:09Z-handoff.json":   `{"handoff":{}}`,
		bucket + "/sess-2/messages/1/0-inference-proposal.json":                `{}`,
	}
	m := &scriptedModel{responses: []string{`{"candidates":[{"content":{"role":"model","parts":[{"text":"[{\"i\":0,\"purpose\":\"deps\",\"all\":[]},{\"i\":1,\"purpose\":\"package\",\"all\":[\"source\",\"package\",\"source\"]}]"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":40,"thoughtsTokenCount":60}}`}}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: m}})
	if err != nil {
		t.Fatal(err)
	}
	deps := PurposeDeps{Client: client, GCS: gcsxtest.NewServer(t, objects).Client, Bucket: bucket}
	got, err := ClassifyPurposes(ctx, deps, sid)
	if err != nil {
		t.Fatalf("ClassifyPurposes: %v", err)
	}
	want := &schema.SessionPurposes{
		Model: llm.GeminiFlash,
		Usage: &schema.TokenUsage{Input: 900, Output: 100, Model: llm.GeminiFlash},
		Calls: []schema.CallPurpose{
			{Object: "2/2026-10-20T00:25:08Z-1-model.json", Call: 1, Purpose: schema.PurposeDeps, All: []schema.Purpose{schema.PurposeDeps}},
			{Object: "2/2026-10-20T00:25:08.5Z-3-model.json", Call: 0, Purpose: schema.PurposePackage, All: []schema.Purpose{schema.PurposePackage, schema.PurposeSource}},
			{Object: "2/2026-10-20T00:25:08.5Z-3-model.json", Call: 1, Purpose: schema.PurposeCompare, All: []schema.Purpose{schema.PurposeCompare}},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ClassifyPurposes (-want +got):\n%s", diff)
	}
	var req struct {
		Contents          []struct{ Parts []struct{ Text string } }
		SystemInstruction struct{ Parts []struct{ Text string } }
		GenerationConfig  struct {
			ResponseMimeType string
			ThinkingConfig   struct{ ThinkingBudget int }
		}
	}
	if err := json.Unmarshal(m.requests[0], &req); err != nil {
		t.Fatal(err)
	}
	if got, want := req.Contents[0].Parts[0].Text, "3 commands from one session, in order.\n\n[0] tool=run_in_container exit=0\n$ npm ci\n> added 12 packages\n\n[1] tool=run_in_container exit=1\n$ npm pack\n> p-1.0.0.tgz\n\n[2] tool=run_on_host exit=?\n$ ls builds\n\n"; got != want {
		t.Errorf("request text:\n%s\nwant:\n%s", got, want)
	}
	if req.SystemInstruction.Parts[0].Text != purposePrompt || req.GenerationConfig.ResponseMimeType != "application/json" || req.GenerationConfig.ThinkingConfig.ThinkingBudget != 256 {
		t.Errorf("request config = %+v; want the purpose prompt, JSON output, and a 256 token thinking budget", req)
	}
	// A transcript without shell calls costs no model call.
	got, err = ClassifyPurposes(ctx, deps, "sess-2")
	if err != nil {
		t.Fatalf("ClassifyPurposes(sess-2): %v", err)
	}
	if diff := cmp.Diff(&schema.SessionPurposes{Model: llm.GeminiFlash, Calls: []schema.CallPurpose{}}, got); diff != "" {
		t.Errorf("ClassifyPurposes(sess-2) (-want +got):\n%s", diff)
	}
	if len(m.requests) != 1 {
		t.Errorf("model requests = %d; want 1", len(m.requests))
	}
}
