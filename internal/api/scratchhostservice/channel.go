// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"encoding/base64"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/errors"
)

// Command is one command to run inside a guest.
type Command struct {
	Argv  []string
	Cwd   string
	Env   map[string]string
	Stdin []byte
}

// ChannelCache holds one ssh channel per guest address. A failed channel is
// evicted, so the next command redials.
type ChannelCache struct {
	dialer *SSHDialer

	mu   sync.Mutex
	open map[string]*sshChannel
}

// NewChannelCache returns a ChannelCache that dials with d.
func NewChannelCache(d *SSHDialer) *ChannelCache {
	return &ChannelCache{dialer: d, open: map[string]*sshChannel{}}
}

// GuestCredential returns the credential for a guest booting at addr.
func (c *ChannelCache) GuestCredential(addr string) (GuestCredential, error) {
	return c.dialer.GuestCredential(addr)
}

// get returns addr's channel, dialing if there is none.
func (c *ChannelCache) get(ctx context.Context, addr string) (*sshChannel, error) {
	c.mu.Lock()
	ch, ok := c.open[addr]
	c.mu.Unlock()
	if ok {
		return ch, nil
	}
	fresh, err := c.dialer.dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Lost a race to dial: share the winner's connection.
	if ch, ok := c.open[addr]; ok {
		c.mu.Unlock()
		_ = fresh.Close()
		return ch, nil
	}
	c.open[addr] = fresh
	c.mu.Unlock()
	return fresh, nil
}

// evict closes ch if it is still addr's channel, so a command failing on a
// stale connection never closes the replacement.
func (c *ChannelCache) evict(addr string, ch *sshChannel) {
	c.mu.Lock()
	cur, ok := c.open[addr]
	if ok && cur == ch {
		delete(c.open, addr)
	}
	c.mu.Unlock()
	if ok && cur == ch {
		_ = ch.Close()
	}
}

// close drops addr's channel, for teardown.
func (c *ChannelCache) close(addr string) {
	c.mu.Lock()
	ch, ok := c.open[addr]
	delete(c.open, addr)
	c.mu.Unlock()
	if ok {
		_ = ch.Close()
	}
}

// run executes cmd on addr. A transport failure evicts the channel but is
// not retried: on the host-only bridge it means the guest's sshd is gone.
func (c *ChannelCache) run(ctx context.Context, addr string, cmd Command, out io.Writer) (int, error) {
	ch, err := c.get(ctx, addr)
	if err != nil {
		return 0, errors.Wrapf(err, "dialing guest %s", addr)
	}
	code, err := ch.Run(ctx, cmd, out)
	if isTransport(err) {
		c.evict(addr, ch)
	}
	return code, err
}

// ErrTransport marks a channel failure before the command ran.
var ErrTransport = errors.New("exec channel transport failure")

func isTransport(err error) bool { return err != nil && errors.Is(err, ErrTransport) }

// transportErr wraps ErrTransport with the failed step and its cause.
func transportErr(what string, err error) error {
	return errors.Wrapf(ErrTransport, "%s: %v", what, err)
}

// runScript rebuilds a command from encodeCommand's fields in $2 and runs it
// in a transient systemd scope named $1, a cgroup a kill can reach whole. It
// is the only shell text a command line carries, so nothing in a command
// needs quoting. read -d "" splits on NUL, which no argv or variable holds.
const runScript = `f=(); while IFS= read -r -d "" x; do f+=("$x"); done < <(base64 -d <<<"$2"); ` +
	`if [ -n "${f[0]}" ]; then cd -- "${f[0]}" || exit; fi; ` +
	`exec env -- "${f[@]:2:${f[1]}}" systemd-run --scope --quiet --collect --unit "$1" -- "${f[@]:$((2+f[1]))}"`

// remoteCommand renders cmd as a command line for the guest's shell: the
// fixed runScript, the unit, and the command encoded in base64, none of
// which a shell expands.
func remoteCommand(cmd Command, unit string) string {
	return "exec bash -c '" + runScript + "' mvm " + unit + " " + encodeCommand(cmd)
}

// encodeCommand joins cmd's fields, each ending in NUL: the directory, the
// number of variables, the variables, and the argv.
func encodeCommand(cmd Command) string {
	var b strings.Builder
	field := func(s string) { b.WriteString(s + "\x00") }
	field(cmd.Cwd)
	field(strconv.Itoa(len(cmd.Env)))
	for _, k := range slices.Sorted(maps.Keys(cmd.Env)) {
		field(k + "=" + cmd.Env[k])
	}
	for _, arg := range cmd.Argv {
		field(arg)
	}
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// syncWriter serializes a session's stdout and stderr copiers into one
// capture.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
