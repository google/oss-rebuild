// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/ssh"
)

const testAddr = "10.200.0.2"

// testDialer returns a broker dialer whose connections reach n.
func testDialer(t *testing.T, n *guestNet) *SSHDialer {
	t.Helper()
	d, err := NewSSHDialer(SSHConfig{User: "root", Port: 22, DialTimeout: 10 * time.Second, Dial: n.dial})
	if err != nil {
		t.Fatalf("NewSSHDialer: %v", err)
	}
	return d
}

// bootFor boots a guest at addr with d's credential, as admission does, and
// returns the credential.
func bootFor(t *testing.T, n *guestNet, d *SSHDialer, addr string) GuestCredential {
	t.Helper()
	cred, err := d.GuestCredential(addr)
	if err != nil {
		t.Fatalf("GuestCredential(%s): %v", addr, err)
	}
	n.up(addr, cred)
	return cred
}

// testChannel dials a guest booted at testAddr.
func testChannel(t *testing.T, n *guestNet) *sshChannel {
	t.Helper()
	d := testDialer(t, n)
	bootFor(t, n, d, testAddr)
	ch, err := d.dial(context.Background(), testAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

func TestSSHDialer_ReachesOnlyGuestsBootedWithItsKey(t *testing.T) {
	n := newGuestNet(t)
	ours, theirs := testDialer(t, n), testDialer(t, n)
	cred := bootFor(t, n, ours, testAddr)
	// The guest serves the host key theirs pinned but authorizes only ours,
	// as a guest from an earlier broker process would.
	pinned, err := theirs.GuestCredential(testAddr)
	if err != nil {
		t.Fatalf("GuestCredential: %v", err)
	}
	n.up(testAddr, GuestCredential{AuthorizedKey: cred.AuthorizedKey, HostKey: pinned.HostKey})
	if _, err := theirs.dial(context.Background(), testAddr); !isTransport(err) || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("dial with another broker's key = %v; want an authentication failure", err)
	}
}

func TestSSHDialer_ReachesOnlyTheGuestBootedAtTheAddress(t *testing.T) {
	n := newGuestNet(t)
	d := testDialer(t, n)
	dial := func(answering string, wantOK bool) {
		t.Helper()
		ch, err := d.dial(context.Background(), testAddr)
		if err == nil {
			_ = ch.Close()
		}
		switch {
		case wantOK && err != nil:
			t.Errorf("dial answered by %s: %v; want success", answering, err)
		case !wantOK && (!isTransport(err) || !strings.Contains(err.Error(), "host key mismatch")):
			t.Errorf("dial answered by %s: %v; want a host key mismatch", answering, err)
		}
	}
	if _, err := d.dial(context.Background(), testAddr); !isTransport(err) {
		t.Errorf("dial before any boot: %v; want a transport error", err)
	}
	here := bootFor(t, n, d, testAddr)
	neighbor, err := d.GuestCredential("10.200.0.3")
	if err != nil {
		t.Fatalf("GuestCredential: %v", err)
	}
	dial("the guest booted there", true)
	n.up(testAddr, neighbor)
	dial("another slot's guest", false)
	bootFor(t, n, d, testAddr)
	dial("the address's new guest", true)
	n.up(testAddr, here)
	dial("the address's previous guest", false)
}

func TestSSHDialer_BoundsTheHandshake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration // the caller's deadline
		want    time.Duration
	}{
		{"CallerAllowsLonger", time.Hour, 10 * time.Second},
		{"CallerAllowsLess", 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newGuestNet(t)
				d := testDialer(t, n)
				bootFor(t, n, d, testAddr)
				n.setStall(true)
				ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
				defer cancel()
				start := time.Now()
				_, err := d.dial(ctx, testAddr)
				if !isTransport(err) {
					t.Errorf("dial to a stalled sshd = %v; want a transport error", err)
				}
				if got := time.Since(start); got != tc.want {
					t.Errorf("dial gave up after %v; want %v", got, tc.want)
				}
			})
		})
	}
}

func TestSSHChannel_RunsTheCommandAsSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  Command
	}{
		{"Plain", Command{Argv: []string{"echo", "hi"}}},
		{"Quotes", Command{Argv: []string{"echo", `it's`, `"double"`, `'`}}},
		{"ShellMetacharacters", Command{Argv: []string{"echo", "a; echo injected", "$(echo injected)", "`echo injected`", "*", "&&", "|", "#"}}},
		{"Newlines", Command{Argv: []string{"printf", "a\nb\n"}}},
		{"Cwd", Command{Argv: []string{"ls"}, Cwd: "/a b/'c'"}},
		{"Env", Command{Argv: []string{"env"}, Env: map[string]string{"A": "1", "B": `v'x y`, "C": "a=b"}}},
		{"Stdin", Command{Argv: []string{"cat"}, Stdin: []byte("piped\x00bytes")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newGuestNet(t)
			ch := testChannel(t, n)
			if code, err := ch.Run(context.Background(), tc.cmd, io.Discard); code != 0 || err != nil {
				t.Fatalf("Run = (%d, %v); want a clean exit", code, err)
			}
			if diff := cmp.Diff([]Command{tc.cmd}, n.ran()); diff != "" {
				t.Errorf("command the guest ran mismatch (-sent +ran):\n%s", diff)
			}
		})
	}
}

func TestSSHChannel_ReportsHowTheCommandEnded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		end       guestExit
		want      int
		wantError bool
	}{
		{"Success", guestExit{}, 0, false},
		{"Status", guestExit{status: 7}, 7, false},
		{"Segfault", guestExit{signal: ssh.SIGSEGV}, 139, false},
		{"Killed", guestExit{signal: ssh.SIGKILL}, 137, false},
		{"NoStatus", guestExit{silent: true}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newGuestNet(t)
			n.setRun(func(context.Context, Command, io.Writer, io.Writer) guestExit { return tc.end })
			code, err := testChannel(t, n).Run(context.Background(), Command{Argv: []string{"x"}}, io.Discard)
			if code != tc.want || (err != nil) != tc.wantError || isTransport(err) {
				t.Errorf("Run = (%d, %v); want (%d, error %v), never a transport failure", code, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestSSHChannel_MergesStdoutAndStderr(t *testing.T) {
	n := newGuestNet(t)
	n.setRun(func(_ context.Context, _ Command, stdout, stderr io.Writer) guestExit {
		for range 100 {
			_, _ = io.WriteString(stdout, "o")
			_, _ = io.WriteString(stderr, "e")
		}
		return guestExit{}
	})
	var out strings.Builder
	if _, err := testChannel(t, n).Run(context.Background(), Command{Argv: []string{"x"}}, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); strings.Count(got, "o") != 100 || strings.Count(got, "e") != 100 {
		t.Errorf("output = %q; want both streams whole", got)
	}
}

func TestSSHChannel_KillsTheScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		end      func(context.CancelFunc) // ends the caller's context
		timeout  time.Duration
		want     int
		wantErr  error
		wantTook time.Duration
	}{
		{"Cancel", func(cancel context.CancelFunc) { cancel() }, time.Hour, 0, context.Canceled, 0},
		{"Deadline", func(context.CancelFunc) {}, 30 * time.Second, 124, context.DeadlineExceeded, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newGuestNet(t)
				running, stopped := make(chan struct{}), make(chan struct{})
				n.setRun(func(ctx context.Context, _ Command, _, _ io.Writer) guestExit {
					close(running)
					<-ctx.Done()
					close(stopped)
					return guestExit{}
				})
				ch := testChannel(t, n)
				ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
				defer cancel()
				go func() {
					<-running
					tc.end(cancel)
				}()
				start := time.Now()
				code, err := ch.Run(ctx, Command{Argv: []string{"sleep", "forever"}}, io.Discard)
				if code != tc.want || err != tc.wantErr {
					t.Errorf("Run = (%d, %v); want (%d, %v)", code, err, tc.want, tc.wantErr)
				}
				if got := time.Since(start); got != tc.wantTook {
					t.Errorf("Run took %v; want %v, with no wait on a killed scope", got, tc.wantTook)
				}
				synctest.Wait()
				select {
				case <-stopped:
				default:
					t.Error("the command still runs in the guest; want its scope killed")
				}
			})
		})
	}
}

func TestSSHChannel_ReturnsAtTheDeadlineThoughTheKillMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newGuestNet(t)
		// The kill lands, but the command outlives it, as when the kill raced
		// the scope's creation.
		release := make(chan struct{})
		defer close(release)
		n.setRun(func(context.Context, Command, io.Writer, io.Writer) guestExit {
			<-release
			return guestExit{}
		})
		ch := testChannel(t, n)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		start := time.Now()
		if code, err := ch.Run(ctx, Command{Argv: []string{"x"}}, io.Discard); code != 124 || err != context.DeadlineExceeded {
			t.Errorf("Run = (%d, %v); want the deadline's (124, DeadlineExceeded)", code, err)
		}
		if got := time.Since(start); got != time.Minute {
			t.Errorf("Run took %v; want it to return at the deadline", got)
		}
	})
}
