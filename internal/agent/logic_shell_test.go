// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/option"
	"google.golang.org/genai"
)

// TestPosixSingleQuote round-trips tricky commands through a real /bin/sh: the
// quoted word must reproduce the input verbatim, with no expansion of $(...),
// backticks, or embedded quotes. run_in_container relies on this to pass the
// LLM's command into `docker exec ... /bin/sh -c <quoted>` without the outer
// shell interpreting it.
func TestPosixSingleQuote(t *testing.T) {
	for _, in := range []string{
		`echo hi`,
		`echo 'single quoted'`,
		`python3 -c "import sys; print('ok')"`,
		`a'b'c`,
		`$(touch pwned)`,
		"`whoami`",
		`back\slash and "double"`,
		"tab\tand space",
	} {
		script := "printf %s " + posixSingleQuote(in)
		out, err := exec.Command("/bin/sh", "-c", script).Output()
		if err != nil {
			t.Fatalf("sh -c %q: %v", script, err)
		}
		if string(out) != in {
			t.Errorf("round-trip mismatch: got %q want %q", out, in)
		}
	}
}

// TestDockerExecInContainerScript pins the wrapper's structure: resolve the
// single retained rb-* container, guard against no build having run, start it
// idempotently, and exec the (safely quoted) command inside it.
func TestDockerExecInContainerScript(t *testing.T) {
	s := dockerExecInContainerScript(`echo 'hi'`)
	for _, want := range []string{
		`docker ps -aq --filter name=^rb-`,
		`exit 125`,
		`docker start "$c"`,
		`exec docker exec -w /src "$c" /bin/sh -c '`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// TestShellTimeout covers the arg coercion: JSON numbers arrive as float64,
// zero means "use the default", and values past the cap clamp to it.
func TestShellTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    int
		wantErr bool
	}{
		{name: "Absent", args: map[string]any{}, want: 0},
		{name: "JSONNumber", args: map[string]any{"timeout_seconds": float64(90)}, want: 90},
		{name: "Int", args: map[string]any{"timeout_seconds": 45}, want: 45},
		{name: "ClampedToMax", args: map[string]any{"timeout_seconds": float64(runCommandTimeoutMax + 1)}, want: runCommandTimeoutMax},
		{name: "NonNumeric", args: map[string]any{"timeout_seconds": "60"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errStr := shellTimeout(tc.args)
			if (errStr != "") != tc.wantErr {
				t.Fatalf("shellTimeout() error = %q, wantErr %v", errStr, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("shellTimeout() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDetachOutput runs the wrapper through a real /bin/sh: the call returns
// when the command's own shell exits even though the child is backgrounded and
// still holds the output, and the output and exit code intact. A wrapper
// killed before its replay, as on a timeout, leaves the output for
// readbackScript.
// NOTE: The child outlives the call, so the test remains fast.
func TestDetachOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	file := filepath.Join(t.TempDir(), "out")
	start := time.Now()
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", detachOutput("echo out; echo err >&2; sleep 1 & exit 3", file)).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit code 3", err)
	}
	if got := string(out); got != "out\nerr\n" {
		t.Errorf("output = %q, want the command's stdout and stderr in order", got)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("call took %v, want a return when the shell exits, not when its child does", took)
	}
	wrapper := exec.CommandContext(ctx, "/bin/sh", "-c", detachOutput("echo partial; sleep 2", file))
	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	for b, _ := os.ReadFile(file); len(b) == 0 && ctx.Err() == nil; b, _ = os.ReadFile(file) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = wrapper.Process.Kill()
	_ = wrapper.Wait()
	out, err = exec.CommandContext(ctx, "/bin/sh", "-c", readbackScript(file)).CombinedOutput()
	if err != nil || string(out) != "partial\n" {
		t.Errorf("readback = %q, %v, want the output so far", out, err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("output file after the readback: %v, want it removed", err)
	}
}

// TestTruncateEnds pins the shape of a cut result: nothing changes until the
// output exceeds head+tail, then the middle is replaced by a marker that
// counts what it hides.
func TestTruncateEnds(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		wantOmitted    int
	}{
		{"fits", "abcd", "abcd", 0},
		{"at the limit", "abcde", "abcde", 0},
		{"cut", "abMIDDLEcde", "ab\n...[6 bytes omitted, narrow the output with head, tail, or grep]...\ncde", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, omitted := truncateEnds(tc.in, 2, 3)
			if got != tc.want || omitted != tc.wantOmitted {
				t.Errorf("truncateEnds(%q, 2, 3) = %q, %d; want %q, %d", tc.in, got, omitted, tc.want, tc.wantOmitted)
			}
		})
	}
}

func TestTextOf(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []*genai.Part
		want  string
	}{
		{"JoinsASplitScript", []*genai.Part{{Text: "node -e 'const"}, {Text: " x = 1'\n"}}, "node -e 'const x = 1'\n"},
		{"SkipsThoughtsAndNil", []*genai.Part{{Text: "hidden", Thought: true}, {Text: "ls"}, nil}, "ls"},
		{"OnlyThoughts", []*genai.Part{{Text: "hidden", Thought: true}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := textOf(tc.parts); got != tc.want {
				t.Errorf("textOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeGCS serves the multipart object upload the storage client uses for
// small writes, keeping each object's content by name.
type fakeGCS struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeGCS(t *testing.T) (*fakeGCS, *gcs.Client) {
	t.Helper()
	f := &fakeGCS{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	client, err := gcs.NewClient(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return f, client
}

func (f *fakeGCS) serve(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	meta, err := mr.NextPart()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(meta).Decode(&obj); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := mr.NextPart()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.objects[obj.Name] = content
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q,"size":"%d"}`, obj.Name, len(content))
}

func (f *fakeGCS) record(t *testing.T, name string) schema.AgentToolCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[name]
	if !ok {
		t.Fatalf("no object at %s, uploaded: %v", name, slices.Sorted(maps.Keys(f.objects)))
	}
	var rec schema.AgentToolCall
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return rec
}

// Each record ties the command to its exec, output object, and shown bytes.
func TestToolCallRecords(t *testing.T) {
	store, client := newFakeGCS(t)
	sh := &fakeShell{
		result: CommandResult{
			ExitCode:   2,
			Output:     strings.Repeat("x", 40),
			ExecID:     "exec-7",
			OutURI:     "gs://exec-out/s1/exec-7.out",
			TotalBytes: 500,
			StartedAt:  time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC),
		},
		logs: []byte(strings.Repeat("L", logsTailLimit+500)),
	}
	a := NewDefaultAgent(testTarget, &AgentDeps{Shell: sh, GCSClient: client, ShellHeadBytes: 4, ShellTailBytes: 6})
	a.toolOpts = &ProposeOpts{ChatUploadURL: &url.URL{Scheme: "gs", Host: "sessions", Path: "sess-1/messages/3"}, Session: "sess-1", Iteration: 3}

	a.runShell(context.Background(), "run_in_container", map[string]any{"command": "ls -la"}, dockerExecInContainerScript)
	rec := store.record(t, toolCallPath("sess-1", 3, 0, "run_in_container"))
	seen, _ := truncateEnds(sh.result.Output, 4, 6)
	want := schema.AgentToolCall{
		SessionID: "sess-1", Iteration: 3, Seq: 0, Tool: "run_in_container",
		Command: "ls -la", ExecID: "exec-7",
		OutURI: "gs://exec-out/s1/exec-7.out", ExitCode: 2, TotalBytes: 500,
		SeenBytes: len(seen), OmittedBytes: 500 - len(seen), StartedAt: sh.result.StartedAt,
	}
	if rec != want {
		t.Errorf("run_in_container record = %+v, want %+v", rec, want)
	}

	a.readLogsTool().Function(context.Background(), nil)
	rec = store.record(t, toolCallPath("sess-1", 3, 1, "read_logs_end"))
	// Scratch-mode logs have no object of their own.
	if rec.OutURI != "" || rec.ExecID != "" {
		t.Errorf("read_logs_end record = %+v, want no exec or object", rec)
	}
	if rec.TotalBytes != int64(len(sh.logs)) || rec.SeenBytes <= logsTailLimit || rec.OmittedBytes != len(sh.logs)-rec.SeenBytes {
		t.Errorf("read_logs_end record = %+v, want the tail of %d bytes", rec, len(sh.logs))
	}

	// The next iteration writes to its own folder, from Seq 0 again, as Propose sets it.
	a.toolOpts, a.toolSeq = &ProposeOpts{ChatUploadURL: &url.URL{Scheme: "gs", Host: "sessions", Path: "sess-1/messages/4"}, Session: "sess-1", Iteration: 4}, 0
	a.runShell(context.Background(), "run_on_host", map[string]any{"command": "docker ps"}, nil)
	if rec := store.record(t, toolCallPath("sess-1", 4, 0, "run_on_host")); rec.Iteration != 4 || rec.Seq != 0 {
		t.Errorf("record after a new iteration = %+v, want iteration 4 seq 0", rec)
	}
}

func TestToolCallRecordsSkippedWithoutTranscript(t *testing.T) {
	sh := &fakeShell{result: CommandResult{Output: "ok"}}
	a := NewDefaultAgent(testTarget, &AgentDeps{Shell: sh})
	a.toolOpts = &ProposeOpts{}
	if resp := a.runShell(context.Background(), "run_on_host", map[string]any{"command": "ls"}, nil); resp.Response["output"] != "ok" {
		t.Errorf("response = %v, want the command's output", resp.Response)
	}
}

func toolNames(defs []*llm.FunctionDefinition) map[string]bool {
	m := map[string]bool{}
	for _, d := range defs {
		m[d.Name] = true
	}
	return m
}

func TestGetToolsWithdrawsHostShellInSingleEnv(t *testing.T) {
	two := NewDefaultAgent(testTarget, &AgentDeps{Shell: &ScratchRunner{}})
	if n := toolNames(two.getTools()); !n["run_on_host"] || !n["run_in_container"] {
		t.Errorf("two-environment tools = %v, want both shell tools", n)
	}
	one := NewDefaultAgent(testTarget, &AgentDeps{Shell: &ScratchRunner{}, SingleEnv: true})
	n := toolNames(one.getTools())
	if n["run_on_host"] {
		t.Error("single-environment tools include run_on_host")
	}
	if !n["run_in_container"] {
		t.Error("single-environment tools drop run_in_container")
	}
}

// fakeShell answers every command with a scripted result, keeping the
// scripts it was dispatched.
type fakeShell struct {
	scripts []string
	result  CommandResult
	logs    []byte
}

func (f *fakeShell) RunCommand(_ context.Context, command string, _ int) (CommandResult, error) {
	f.scripts = append(f.scripts, command)
	return f.result, nil
}

func (f *fakeShell) LastBuildLogs(context.Context) ([]byte, error) { return f.logs, nil }
