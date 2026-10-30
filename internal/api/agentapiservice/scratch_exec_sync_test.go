// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"fmt"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/api/scratchhostservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
)

// schedule mirroring DefaultSyncSchedule's shape, but with named
// constants so test cases below stay readable as the default changes.
var testSchedule = []SyncStep{
	{Until: 10 * time.Minute, Interval: 5 * time.Second},
	// Past 10 min: 5s remains the last step's Interval. Overridden
	// below to confirm the boundary behavior matches the documented
	// "last interval applies indefinitely" contract for tests that
	// care.
}

func TestSyncer_IntervalAt_DefaultSchedule(t *testing.T) {
	s := &gcsSyncer{schedule: DefaultSyncSchedule}
	tests := []struct {
		name string
		age  time.Duration
		want time.Duration
	}{
		{"at start", 0, 5 * time.Second},
		{"30s in", 30 * time.Second, 5 * time.Second},
		{"5m in", 5 * time.Minute, 5 * time.Second},
		{"just under 10m boundary", 10*time.Minute - time.Nanosecond, 5 * time.Second},
		{"at 10m boundary", 10 * time.Minute, 30 * time.Second},
		{"1h in", time.Hour, 30 * time.Second},
		{"6h in", 6 * time.Hour, 30 * time.Second},
		{"24h in (well past last step)", 24 * time.Hour, 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.intervalAt(tc.age); got != tc.want {
				t.Errorf("intervalAt(%v) = %v, want %v", tc.age, got, tc.want)
			}
		})
	}
}

func TestSyncer_IntervalAt_MultiStep(t *testing.T) {
	// A 3-step schedule to confirm bucket-walking handles >1 boundary.
	s := &gcsSyncer{schedule: []SyncStep{
		{Until: time.Minute, Interval: time.Second},
		{Until: 10 * time.Minute, Interval: 5 * time.Second},
		{Until: time.Hour, Interval: 30 * time.Second},
	}}
	tests := []struct {
		age  time.Duration
		want time.Duration
	}{
		{0, time.Second},
		{59 * time.Second, time.Second},
		{time.Minute, 5 * time.Second},
		{9 * time.Minute, 5 * time.Second},
		{10 * time.Minute, 30 * time.Second},
		{59 * time.Minute, 30 * time.Second},
		{time.Hour, 30 * time.Second},      // past last Until: stick at last Interval
		{24 * time.Hour, 30 * time.Second}, // way past: still last Interval
	}
	for _, tc := range tests {
		if got := s.intervalAt(tc.age); got != tc.want {
			t.Errorf("intervalAt(%v) = %v, want %v", tc.age, got, tc.want)
		}
	}
}

func TestSyncer_ShouldCompose(t *testing.T) {
	// Tight test schedule: 1s for the first 10s, 5s thereafter. Lets
	// every case land in single-digit-second deltas.
	s := &gcsSyncer{schedule: []SyncStep{
		{Until: 10 * time.Second, Interval: time.Second},
		{Until: math.MaxInt64, Interval: 5 * time.Second},
	}}
	// "started" is the reference point for age computations below.
	started := time.Unix(1_700_000_000, 0).UTC()

	tests := []struct {
		name         string
		now          time.Time
		lastModified time.Time
		done         bool
		want         bool
	}{
		{
			name:         "first sync, no prior object",
			now:          started.Add(2 * time.Second),
			lastModified: time.Time{}, // zero → sinceLastCompose huge
			done:         false,
			want:         true,
		},
		{
			name:         "second sync, just composed within first-step interval",
			now:          started.Add(2 * time.Second),
			lastModified: started.Add(1*time.Second + 500*time.Millisecond), // 500ms ago, interval=1s
			done:         false,
			want:         false,
		},
		{
			name:         "interval elapsed in first step",
			now:          started.Add(3 * time.Second),
			lastModified: started.Add(2 * time.Second), // 1s ago, interval=1s → equal, run
			done:         false,
			want:         true,
		},
		{
			name:         "in slow step, recently composed",
			now:          started.Add(20 * time.Second),
			lastModified: started.Add(17 * time.Second), // 3s ago, interval=5s
			done:         false,
			want:         false,
		},
		{
			name:         "in slow step, interval elapsed",
			now:          started.Add(20 * time.Second),
			lastModified: started.Add(15 * time.Second), // 5s ago, interval=5s
			done:         false,
			want:         true,
		},
		{
			name:         "done overrides skip in fast step",
			now:          started.Add(2 * time.Second),
			lastModified: started.Add(1*time.Second + 500*time.Millisecond), // 500ms ago
			done:         true,
			want:         true,
		},
		{
			name:         "done overrides skip in slow step",
			now:          started.Add(20 * time.Second),
			lastModified: started.Add(19 * time.Second), // 1s ago, interval=5s
			done:         true,
			want:         true,
		},
		{
			name:         "exec age past last step uses last interval",
			now:          started.Add(time.Hour),
			lastModified: started.Add(time.Hour - 3*time.Second), // 3s ago, interval=5s
			done:         false,
			want:         false,
		},
		{
			name:         "exec age past last step, interval elapsed",
			now:          started.Add(time.Hour),
			lastModified: started.Add(time.Hour - 6*time.Second), // 6s ago, interval=5s
			done:         false,
			want:         true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.shouldCompose(tc.now, started, tc.lastModified, tc.done); got != tc.want {
				t.Errorf("shouldCompose(now+%v, lastMod=%v, done=%v) = %v, want %v",
					tc.now.Sub(started), tc.lastModified.Sub(started), tc.done, got, tc.want)
			}
		})
	}
}

func TestSyncer_FinalFromStatus(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	workerStart := created.Add(2 * time.Second)
	workerFinish := workerStart.Add(30 * time.Second)
	brokerNow := created.Add(45 * time.Second)
	base := schema.ScratchExec{
		ID:        "op1",
		ScratchID: "s1",
		State:     schema.ScratchExecPending,
		CreatedAt: created,
	}
	tests := []struct {
		name   string
		status scratchhostservice.ExecStatus
		want   schema.ScratchExec
	}{
		{
			name: "WorkerClocksAdopted",
			status: scratchhostservice.ExecStatus{
				Done:       true,
				ExitCode:   3,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
			},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecCompleted,
				ExitCode:   3,
				CreatedAt:  created,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
				Updated:    brokerNow,
			},
		},
		{
			// A worker that never observed a start leaves StartedAt zero.
			// Only FinishedAt falls back to the broker clock.
			name:   "MissingWorkerClocksFallBack",
			status: scratchhostservice.ExecStatus{Done: true},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecCompleted,
				CreatedAt:  created,
				FinishedAt: brokerNow,
				Updated:    brokerNow,
			},
		},
		{
			name: "TimedOut",
			status: scratchhostservice.ExecStatus{
				Done:       true,
				ExitCode:   124,
				TimedOut:   true,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
			},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecTimedOut,
				ExitCode:   124,
				CreatedAt:  created,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
				Updated:    brokerNow,
				Error: &schema.Status{
					Code:    int(codes.DeadlineExceeded),
					Message: "command exceeded TimeoutSeconds",
				},
			},
		},
		{
			// Terminal like a timeout, with its own code.
			name: "OutputCapped",
			status: scratchhostservice.ExecStatus{
				Done:         true,
				OutputCapped: true,
				TotalBytes:   256 << 20,
				StartedAt:    workerStart,
				FinishedAt:   workerFinish,
			},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecTimedOut,
				CreatedAt:  created,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
				Updated:    brokerNow,
				Error: &schema.Status{
					Code:    int(codes.ResourceExhausted),
					Message: "output exceeded 268435456 bytes",
				},
			},
		},
		{
			// A killed op lands in Lost with an Aborted error.
			name: "KilledIsLost",
			status: scratchhostservice.ExecStatus{
				Done:       true,
				Killed:     true,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
			},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecLost,
				CreatedAt:  created,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
				Updated:    brokerNow,
				Error: &schema.Status{
					Code:    int(codes.Aborted),
					Message: "op killed",
				},
			},
		},
		{
			name: "InfraErrorIsLost",
			status: scratchhostservice.ExecStatus{
				Done:       true,
				ErrMsg:     "spawn failed",
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
			},
			want: schema.ScratchExec{
				ID:         "op1",
				ScratchID:  "s1",
				State:      schema.ScratchExecLost,
				CreatedAt:  created,
				StartedAt:  workerStart,
				FinishedAt: workerFinish,
				Updated:    brokerNow,
				Error: &schema.Status{
					Code:    int(codes.Internal),
					Message: "spawn failed",
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := finalFromStatus(base, &tc.status, brokerNow)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("finalFromStatus mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Sanity check the DefaultSyncSchedule's documented cumulative compose
// counts so a future tweak doesn't silently drift past the 1024 cap.
func TestSyncer_DefaultScheduleBudget(t *testing.T) {
	// Worst case (ignoring race losses) is one compose per interval
	// at each age slice. Compute cumulative count at the documented
	// checkpoints and assert plenty of headroom under 1024.
	s := &gcsSyncer{schedule: DefaultSyncSchedule}
	checkpoints := []struct {
		age      time.Duration
		maxCount int
	}{
		{10 * time.Minute, 130},
		{time.Hour, 240},
		{6 * time.Hour, 900},
	}
	cumulative := 0
	prev := time.Duration(0)
	cursor := time.Duration(0)
	for _, cp := range checkpoints {
		// Walk from cursor → cp.age, counting composes at each
		// interval boundary.
		for cursor < cp.age {
			interval := s.intervalAt(cursor)
			cursor += interval
			if cursor <= cp.age {
				cumulative++
			}
		}
		if cumulative > cp.maxCount {
			t.Errorf("at age %v: cumulative composes = %d, want <= %d (slice from %v)", cp.age, cumulative, cp.maxCount, prev)
		}
		prev = cp.age
	}
	if cumulative >= 1024 {
		t.Errorf("schedule consumes full 1024-component budget by 6h (got %d); leave headroom for races + the rewrite backstop", cumulative)
	}
}

func TestSyncer_TerminalSyncDefersUntilOutputComplete(t *testing.T) {
	ctx := context.Background()
	const total = 22
	// The output object is absent until size is set, and every write fails
	// its precondition, as when a concurrent sync owns the tail.
	var size atomic.Int64
	size.Store(-1)
	gcs := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch n := size.Load(); {
		case r.Method == http.MethodGet && n >= 0:
			fmt.Fprintf(rw, `{"name":"out","bucket":"b","size":"%d","generation":"1"}`, n)
		case r.Method == http.MethodGet:
			http.NotFound(rw, r)
		case r.Method == http.MethodPost:
			http.Error(rw, `{"error":{"code":412}}`, http.StatusPreconditionFailed)
		}
	}))
	defer gcs.Close()
	client, err := storage.NewClient(ctx, option.WithoutAuthentication(), option.WithEndpoint(gcs.URL))
	if err != nil {
		t.Fatal(err)
	}
	// The host is done. Its stream can be empty since no pull lands.
	mux := http.NewServeMux()
	mux.HandleFunc("/exec/op/status", api.Handler(api.NoDepsInit, func(context.Context, scratchhostservice.OpRequest, *api.NoDeps) (*scratchhostservice.ExecStatus, error) {
		return &scratchhostservice.ExecStatus{Done: true, TotalBytes: total}, nil
	}))
	mux.HandleFunc("/exec/op/output", api.StreamHandler(api.NoDepsInit, func(context.Context, scratchhostservice.OutputRequest, *api.NoDeps) iter.Seq2[*scratchhostservice.OutputFrame, error] {
		return func(func(*scratchhostservice.OutputFrame, error) bool) {}
	}))
	host := httptest.NewServer(mux)
	defer host.Close()
	dial := func(Instance) (httpx.BasicClient, *url.URL, error) {
		u, err := url.Parse(host.URL)
		return host.Client(), u, err
	}
	execs := db.NewMemoryScratchExecs()
	exec := schema.ScratchExec{ID: "op-1", ScratchID: "s-1", State: schema.ScratchExecPending}
	if err := execs.Insert(ctx, exec); err != nil {
		t.Fatal(err)
	}
	syncer := NewGCSSyncer(client, "b", execs, dial)
	scratch := schema.Scratch{ID: "s-1", ObliviousID: "obl"}

	// The pull loses its race and the winner's compose has not landed.
	if _, err := syncer.Sync(ctx, exec, scratch); err != nil {
		t.Fatal(err)
	}
	if got, _ := execs.Get(ctx, "op-1"); got.State != schema.ScratchExecPending {
		t.Fatalf("state with output short = %q, want pending", got.State)
	}
	// The winner's compose lands.
	size.Store(total)
	if _, err := syncer.Sync(ctx, exec, scratch); err != nil {
		t.Fatal(err)
	}
	if got, _ := execs.Get(ctx, "op-1"); got.State != schema.ScratchExecCompleted {
		t.Errorf("state with output complete = %q, want completed", got.State)
	}
}
