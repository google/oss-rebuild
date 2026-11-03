// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"encoding/base64"
	"io"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func start(t *testing.T, deps *GuestDeps, req StartRequest) {
	t.Helper()
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
}

// awaitDone is waitDone over the status handler.
func awaitDone(t *testing.T, deps *GuestDeps, opID string) ExecStatus {
	t.Helper()
	return waitDone(t, func() (ExecStatus, bool) {
		st, err := Status(context.Background(), OpRequest{ID: opID}, deps)
		if err != nil {
			return ExecStatus{}, false
		}
		return *st, true
	})
}

// drain is collect over the output handler.
func drain(t *testing.T, deps *GuestDeps, opID string, offset int64) ([]byte, []int64, error) {
	t.Helper()
	return collect(t, Output(context.Background(), OutputRequest{ID: opID, Offset: offset}, deps))
}

func TestExec_RoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		n := guestsOn(t, deps)
		admit(t, deps, "scratch-a")
		n.setRun(func(_ context.Context, _ Command, stdout, _ io.Writer) guestExit {
			_, _ = io.WriteString(stdout, "output")
			return guestExit{status: 7}
		})
		start(t, deps, StartRequest{
			OpID: "op-1", ScratchID: "scratch-a",
			Cmd: []string{"echo", "hi"}, Cwd: "/work",
			Env:            map[string]string{"K": "V"},
			StdinB64:       base64.StdEncoding.EncodeToString([]byte("piped")),
			TimeoutSeconds: 60,
		})
		st := awaitDone(t, deps, "op-1")
		if st.ExitCode != 7 || st.TimedOut || st.Killed || st.ErrMsg != "" {
			t.Errorf("status = %+v; want a clean exit 7", st)
		}
		got, offsets, err := drain(t, deps, "op-1", 0)
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if string(got) != "output" {
			t.Errorf("output = %q; want the guest's", got)
		}
		want := Command{Argv: []string{"echo", "hi"}, Cwd: "/work", Env: map[string]string{"K": "V"}, Stdin: []byte("piped")}
		if diff := cmp.Diff([]Command{want}, n.ran()); diff != "" {
			t.Errorf("command the guest ran mismatch (-sent +ran):\n%s", diff)
		}
		if len(offsets) != 1 || offsets[0] != 0 {
			t.Errorf("offsets = %v; want a single frame at 0", offsets)
		}
		if int64(len(got)) != st.TotalBytes {
			t.Errorf("TotalBytes = %d; want %d", st.TotalBytes, len(got))
		}
	})
}

func TestExec_DefaultsCwdToWorkdir(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		n := guestsOn(t, deps)
		admit(t, deps, "scratch-a")
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"pwd"}, TimeoutSeconds: 60})
		awaitDone(t, deps, "op")
		if ran := n.ran(); len(ran) != 1 || ran[0].Cwd != "/home/builder" {
			t.Errorf("guest ran %+v; want pwd in the configured workdir", ran)
		}
	})
}

func TestExec_StreamsWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		n := guestsOn(t, deps)
		admit(t, deps, "scratch-a")
		release := make(chan struct{})
		n.setRun(func(_ context.Context, _ Command, stdout, _ io.Writer) guestExit {
			_, _ = io.WriteString(stdout, "first half ")
			<-release
			_, _ = io.WriteString(stdout, "second half")
			return guestExit{}
		})
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"slow"}, TimeoutSeconds: 60})
		// Once the bubble is idle the command is mid-run, blocked on release.
		synctest.Wait()
		mid, _, _ := drain(t, deps, "op", 0)
		if string(mid) != "first half " {
			t.Errorf("mid-run output = %q; want the bytes written so far", mid)
		}
		close(release)
		awaitDone(t, deps, "op")
		rest, offsets, err := drain(t, deps, "op", int64(len(mid)))
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if string(rest) != "second half" || offsets[0] != int64(len(mid)) {
			t.Errorf("tail = %q at %v; want the remainder from the earlier offset", rest, offsets)
		}
	})
}

func TestExec_Kill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		n := guestsOn(t, deps)
		admit(t, deps, "scratch-a")
		running := make(chan struct{})
		n.setRun(func(ctx context.Context, _ Command, _, _ io.Writer) guestExit {
			close(running)
			<-ctx.Done()
			return guestExit{}
		})
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"sleep", "forever"}, TimeoutSeconds: 3600})
		<-running
		if _, err := Kill(context.Background(), OpRequest{ID: "op"}, deps); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if st := awaitDone(t, deps, "op"); !st.Killed || st.TimedOut {
			t.Errorf("status = %+v; want Killed and not TimedOut", st)
		}
		if _, err := Kill(context.Background(), OpRequest{ID: "missing"}, deps); status.Code(err) != codes.NotFound {
			t.Errorf("Kill(missing) = %v; want NotFound", err)
		}
	})
}

func TestExecStart_RequiresALiveGuest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		admit(t, deps, "scratch-a")
		// The guest dies. Its ledger answers for old ops, but nothing new runs.
		killGuest(t, deps)
		_, err := ExecStart(context.Background(), StartRequest{
			OpID: "op", ScratchID: "scratch-a", Cmd: []string{"true"}, TimeoutSeconds: 60,
		}, deps)
		if status.Code(err) != codes.NotFound {
			t.Errorf("ExecStart without a live guest = %v; want NotFound", err)
		}
	})
}

func TestExecStart_UnknownSessionIsNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		// Asking after an unknown session must not create its ledger.
		_, err := ExecStart(context.Background(), StartRequest{
			OpID: "op", ScratchID: "scratch-zzz", Cmd: []string{"true"}, TimeoutSeconds: 60,
		}, deps)
		if status.Code(err) != codes.NotFound {
			t.Errorf("ExecStart for an unknown session = %v; want NotFound", err)
		}
		if _, ok := deps.Host.ledger("scratch-zzz"); ok {
			t.Error("probing an unknown session created its ledger")
		}
		if _, err := Status(context.Background(), OpRequest{ID: "op"}, deps); status.Code(err) != codes.NotFound {
			t.Errorf("Status of an unknown op = %v; want NotFound", err)
		}
	})
}

func TestExec_OutputOutlivesTheGuest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The captured output outlives the guest until teardown.
		deps, _ := testDeps(t)
		admit(t, deps, "scratch-a")
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"echo", "done"}, TimeoutSeconds: 60})
		awaitDone(t, deps, "op")
		// The guest dies without the broker being told.
		killGuest(t, deps)
		if st, err := Status(context.Background(), OpRequest{ID: "op"}, deps); err != nil || st.ExitCode != 0 {
			t.Errorf("Status after guest death = %+v, %v; want the recorded terminal state", st, err)
		}
		got, _, err := drain(t, deps, "op", 0)
		if err != nil || string(got) != "echo done" {
			t.Errorf("Output after guest death = %q, %v; want the captured bytes", got, err)
		}
	})
}

func TestExec_LedgerDiscardedWithTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		admit(t, deps, "scratch-a")
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"echo", "x"}, TimeoutSeconds: 60})
		awaitDone(t, deps, "op")
		if _, err := GuestDelete(context.Background(), GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
			t.Fatalf("GuestDelete: %v", err)
		}
		if _, ok := deps.Host.ledger("scratch-a"); ok {
			t.Error("ledger survived its session's teardown; want its records reclaimed with the guest")
		}
		if _, err := Status(context.Background(), OpRequest{ID: "op"}, deps); status.Code(err) != codes.NotFound {
			t.Errorf("Status after teardown = %v; want NotFound", err)
		}
		// A dead guest's ledger alone makes the session deletable.
		admit(t, deps, "scratch-b")
		start(t, deps, StartRequest{OpID: "op-b", ScratchID: "scratch-b", Cmd: []string{"echo", "y"}, TimeoutSeconds: 60})
		awaitDone(t, deps, "op-b")
		killGuest(t, deps)
		if _, err := GuestDelete(context.Background(), GuestRequest{ScratchID: "scratch-b"}, deps); err != nil {
			t.Errorf("GuestDelete of a dead guest with records = %v; want success", err)
		}
		if _, err := GuestDelete(context.Background(), GuestRequest{ScratchID: "scratch-b"}, deps); status.Code(err) != codes.NotFound {
			t.Errorf("repeat GuestDelete = %v; want NotFound", err)
		}
	})
}

func TestExec_ReadmissionStartsAFreshLedger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := testDeps(t)
		admit(t, deps, "scratch-a")
		start(t, deps, StartRequest{OpID: "op", ScratchID: "scratch-a", Cmd: []string{"echo", "x"}, TimeoutSeconds: 60})
		awaitDone(t, deps, "op")
		// Readmitting the session starts a fresh ledger.
		killGuest(t, deps)
		admit(t, deps, "scratch-a")
		if _, err := Status(context.Background(), OpRequest{ID: "op"}, deps); status.Code(err) != codes.NotFound {
			t.Errorf("old op after re-admission = %v; want NotFound", err)
		}
	})
}
