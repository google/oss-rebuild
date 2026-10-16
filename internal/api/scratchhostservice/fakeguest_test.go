// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

// A fake guest is an in-process sshd that the broker's real dialer and
// channel reach through SSHConfig.Dial. It emulates only what the broker
// relies on from a guest: the keys on its kernel command line, the
// systemd-run scope that remoteCommand wraps a command in, and systemctl kill
// on that scope. Connections are in-memory and channel-buffered, so tests
// run in synctest bubbles.

// guestCmd is what a command does in a fake guest. It ends by returning, and
// the broker's kill cancels ctx.
type guestCmd func(ctx context.Context, cmd Command, stdout, stderr io.Writer) guestExit

// guestExit is how a fake guest's command ends.
type guestExit struct {
	status int        // exit status, when signal is empty
	signal ssh.Signal // the signal it died of
	silent bool       // neither is reported, as when sshd loses the session
}

// echoArgv is the default command. It writes its argv and exits 0.
func echoArgv(_ context.Context, cmd Command, stdout, _ io.Writer) guestExit {
	_, _ = io.WriteString(stdout, strings.Join(cmd.Argv, " "))
	return guestExit{}
}

// guestNet is a host-only bridge of fake guests. Its faults apply to every
// guest on it.
type guestNet struct {
	mu         sync.Mutex
	boots      func(addr string) (cmdline string, halted <-chan struct{}, ok bool) // the guest booted at addr
	booted     map[string]string                                                   // cmdlines from up
	guests     map[string]*fakeGuest                                               // running guest per address
	run        guestCmd
	refuse     bool // dials fail, as before sshd is up
	stall      bool // connections never finish the handshake
	refuseOpen bool // sessions are refused, as past sshd's MaxSessions
	dials      int
	commands   []Command // every command run, in order
}

// newGuestNet returns an empty guestNet whose guests stop when t ends.
func newGuestNet(t *testing.T) *guestNet {
	n := &guestNet{booted: map[string]string{}, guests: map[string]*fakeGuest{}, run: echoArgv}
	n.boots = func(addr string) (string, <-chan struct{}, bool) {
		c, ok := n.booted[addr]
		return c, nil, ok
	}
	t.Cleanup(func() {
		for _, g := range n.running() {
			g.halt()
		}
	})
	return n
}

func (n *guestNet) running() []*fakeGuest {
	n.mu.Lock()
	defer n.mu.Unlock()
	guests := make([]*fakeGuest, 0, len(n.guests))
	for _, g := range n.guests {
		guests = append(guests, g)
	}
	return guests
}

// up boots a guest at addr with cred.
func (n *guestNet) up(addr string, cred GuestCredential) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.booted[addr] = kernelCmdline(GuestConfig{}, GuestNet{IPs: []netip.Addr{netip.MustParseAddr(addr)}}, 0, cred)
}

func (n *guestNet) setRun(run guestCmd)  { n.mu.Lock(); n.run = run; n.mu.Unlock() }
func (n *guestNet) setRefuse(v bool)     { n.mu.Lock(); n.refuse = v; n.mu.Unlock() }
func (n *guestNet) setStall(v bool)      { n.mu.Lock(); n.stall = v; n.mu.Unlock() }
func (n *guestNet) setRefuseOpen(v bool) { n.mu.Lock(); n.refuseOpen = v; n.mu.Unlock() }

func (n *guestNet) dialCount() int { n.mu.Lock(); defer n.mu.Unlock(); return n.dials }

// ran returns every command the guests ran, in order.
func (n *guestNet) ran() []Command {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.commands)
}

// drop closes every connection, as when a guest's sshd dies. The guests
// stay up, so a redial reaches them.
func (n *guestNet) drop() {
	for _, g := range n.running() {
		g.closeConns()
	}
}

// open reports how many connections are open to the guest at addr.
func (n *guestNet) open(addr string) int {
	n.mu.Lock()
	g := n.guests[addr]
	n.mu.Unlock()
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.conns)
}

// dial is SSHConfig.Dial for the bridge.
func (n *guestNet) dial(_ context.Context, _, hostport string) (net.Conn, error) {
	addr, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	cmdline, halted, ok := n.boots(addr)
	if !ok || n.refuse {
		return nil, errors.Errorf("dial %s: connection refused", hostport)
	}
	g := n.guests[addr]
	// A guest is its boot: the same command line can boot the address again.
	if g == nil || g.cmdline != cmdline || g.halted != halted || g.isStopped() {
		if g != nil {
			go g.halt()
		}
		if g, err = bootGuest(n, cmdline, halted); err != nil {
			return nil, err
		}
		n.guests[addr] = g
	}
	n.dials++
	client, server := memPipe()
	go g.serve(server, n.stall)
	return client, nil
}

// fakeGuest is one booted guest's sshd.
type fakeGuest struct {
	net     *guestNet
	cmdline string
	halted  <-chan struct{}
	config  *ssh.ServerConfig

	mu      sync.Mutex
	conns   []net.Conn
	scopes  map[string]context.CancelFunc // running commands by unit
	killed  map[string]bool
	stopped bool
}

// bootGuest starts a guest from its kernel command line: the broker's
// authorized key, and the host key when the line carries one. Otherwise the
// guest has a key of its own. halted, if not nil, stops it.
func bootGuest(n *guestNet, cmdline string, halted <-chan struct{}) (*fakeGuest, error) {
	params := map[string][]byte{}
	for _, kv := range strings.Fields(cmdline) {
		k, v, _ := strings.Cut(kv, "=")
		if k == "mvm_ssh_authorized_key" || k == "mvm_ssh_host_key" {
			b, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, errors.Wrapf(err, "%s", k)
			}
			params[k] = b
		}
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey(params["mvm_ssh_authorized_key"])
	if err != nil {
		return nil, errors.Wrap(err, "authorized key")
	}
	var hostKey ssh.Signer
	if pem, ok := params["mvm_ssh_host_key"]; ok {
		hostKey, err = ssh.ParsePrivateKey(pem)
	} else {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		hostKey, err = ssh.NewSignerFromKey(priv)
	}
	if err != nil {
		return nil, errors.Wrap(err, "host key")
	}
	g := &fakeGuest{net: n, cmdline: cmdline, halted: halted, scopes: map[string]context.CancelFunc{}, killed: map[string]bool{}}
	g.config = &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), authorized.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("not the authorized key")
	}}
	g.config.AddHostKey(hostKey)
	if halted != nil {
		go func() {
			<-halted
			g.halt()
		}()
	}
	return g, nil
}

func (g *fakeGuest) isStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// halt stops the guest: its connections close and its commands end.
func (g *fakeGuest) halt() {
	g.mu.Lock()
	g.stopped = true
	for _, cancel := range g.scopes {
		cancel()
	}
	g.mu.Unlock()
	g.closeConns()
}

func (g *fakeGuest) closeConns() {
	g.mu.Lock()
	conns := g.conns
	g.conns = nil
	g.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (g *fakeGuest) serve(conn net.Conn, stall bool) {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		_ = conn.Close()
		return
	}
	g.conns = append(g.conns, conn)
	g.mu.Unlock()
	defer g.forget(conn)
	defer conn.Close()
	if stall {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	sc, chans, reqs, err := ssh.NewServerConn(conn, g.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		g.net.mu.Lock()
		refuse := g.net.refuseOpen
		g.net.mu.Unlock()
		if nc.ChannelType() != "session" || refuse {
			_ = nc.Reject(ssh.Prohibited, "no more sessions")
			continue
		}
		if ch, creqs, err := nc.Accept(); err == nil {
			go g.session(ch, creqs)
		}
	}
}

func (g *fakeGuest) forget(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conns = slices.DeleteFunc(g.conns, func(c net.Conn) bool { return c == conn })
}

// session runs one session's exec request.
func (g *fakeGuest) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		go ssh.DiscardRequests(reqs)
		g.exec(ch, payload.Command)
		return
	}
}

// exec runs a command line the way the guest's shell, systemd-run and
// systemctl would.
func (g *fakeGuest) exec(ch ssh.Channel, line string) {
	if unit, ok := killTarget(line); ok {
		sendStatus(ch, g.kill(unit, ch.Stderr()))
		return
	}
	cmd, unit, err := parseRun(line)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "fake guest: %v\n", err)
		sendStatus(ch, 127)
		return
	}
	if stdin, _ := io.ReadAll(ch); len(stdin) > 0 {
		cmd.Stdin = stdin
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.mu.Lock()
	g.scopes[unit] = cancel
	g.mu.Unlock()
	g.net.mu.Lock()
	run := g.net.run
	g.net.commands = append(g.net.commands, cmd)
	g.net.mu.Unlock()
	e := run(ctx, cmd, ch, ch.Stderr())
	g.mu.Lock()
	delete(g.scopes, unit)
	killed := g.killed[unit]
	g.mu.Unlock()
	switch {
	case killed:
		sendSignal(ch, ssh.SIGKILL)
	case e.silent:
	case e.signal != "":
		sendSignal(ch, e.signal)
	default:
		sendStatus(ch, e.status)
	}
}

// kill is systemctl kill on a scope. It fails for a unit that is not
// running, as systemctl does.
func (g *fakeGuest) kill(unit string, stderr io.Writer) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	cancel, ok := g.scopes[unit]
	if !ok {
		fmt.Fprintf(stderr, "Failed to kill unit %s.scope: Unit %s.scope not loaded.\n", unit, unit)
		return 1
	}
	g.killed[unit] = true
	cancel()
	return 0
}

func sendStatus(ch ssh.Channel, status int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
}

func sendSignal(ch ssh.Channel, sig ssh.Signal) {
	_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(struct {
		Signal     string
		CoreDumped bool
		Error      string
		Lang       string
	}{Signal: string(sig)}))
}

// plainWord is a word no shell expands: the unit, or base64.
var plainWord = regexp.MustCompile(`^[[:alnum:]+/=-]+$`)

// parseRun reads remoteCommand's line back into a Command and its scope's
// unit. The line must be runScript and two plain words, so no part of the
// command reaches the shell as syntax.
func parseRun(line string) (Command, string, error) {
	args, ok := strings.CutPrefix(line, "exec bash -c '"+runScript+"' mvm ")
	unit, enc, _ := strings.Cut(args, " ")
	if !ok || !plainWord.MatchString(unit) || !plainWord.MatchString(enc) {
		return Command{}, "", errors.Errorf("not a scope run: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return Command{}, "", err
	}
	f := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	n, err := strconv.Atoi(f[1])
	if err != nil || len(f) < 2+n {
		return Command{}, "", errors.Errorf("bad fields: %q", f)
	}
	cmd := Command{Cwd: f[0], Argv: f[2+n:]}
	for _, kv := range f[2 : 2+n] {
		k, v, _ := strings.Cut(kv, "=")
		if cmd.Env == nil {
			cmd.Env = map[string]string{}
		}
		cmd.Env[k] = v
	}
	return cmd, unit, nil
}

// killTarget reads the scope a kill names.
func killTarget(line string) (string, bool) {
	scope, ok := strings.CutPrefix(line, "systemctl kill --signal=KILL ")
	if !ok || !plainWord.MatchString(strings.TrimSuffix(scope, ".scope")) {
		return "", false
	}
	return strings.CutSuffix(scope, ".scope")
}

// memPipe is a net.Pipe whose second end, the guest's, writes without
// waiting for a reader, since both ends of an ssh handshake write first.
// net.Pipe blocks only on channels and timers, which synctest counts as
// durable, and keeps its deadlines.
func memPipe() (net.Conn, net.Conn) {
	client, server := net.Pipe()
	c := &asyncConn{Conn: server, queue: make(chan []byte, 1024), done: make(chan struct{})}
	go c.pump()
	return client, c
}

type asyncConn struct {
	net.Conn
	queue chan []byte
	done  chan struct{}
	once  sync.Once
}

func (c *asyncConn) pump() {
	for {
		select {
		case b := <-c.queue:
			if _, err := c.Conn.Write(b); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *asyncConn) Write(p []byte) (int, error) {
	select {
	case c.queue <- slices.Clone(p):
		return len(p), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *asyncConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
