// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

// DialFunc opens a connection, as net.Dialer.DialContext does.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// SSHConfig configures the ssh exec channel.
type SSHConfig struct {
	User        string        // guest login for the broker's key
	Port        int           // guest sshd port
	DialTimeout time.Duration // also bounds the readiness probe
	Dial        DialFunc      // nil dials TCP
}

// SSHDialer dials guests with one key generated per broker process. Guests
// die with the broker, so nothing outlives the key.
type SSHDialer struct {
	cfg    SSHConfig
	signer ssh.Signer
	pubkey string
}

// NewSSHDialer returns an SSHDialer over a freshly generated ed25519 key.
func NewSSHDialer(cfg SSHConfig) (*SSHDialer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.Wrap(err, "generating channel key")
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, errors.Wrap(err, "signer from key")
	}
	return &SSHDialer{
		cfg:    cfg,
		signer: signer,
		pubkey: string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
	}, nil
}

// GuestCredential returns the broker's authorized_keys line, base64 encoded
// to survive the kernel command line.
func (d *SSHDialer) GuestCredential() string {
	return base64.RawStdEncoding.EncodeToString([]byte(d.pubkey))
}

// dial opens a channel to the guest at addr.
func (d *SSHDialer) dial(ctx context.Context, addr string) (*sshChannel, error) {
	target := net.JoinHostPort(addr, strconv.Itoa(d.cfg.Port))
	dial := d.cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: d.cfg.DialTimeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return nil, transportErr("tcp", err)
	}
	deadline := time.Now().Add(d.cfg.DialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	cc, chans, reqs, err := ssh.NewClientConn(conn, target, &ssh.ClientConfig{
		User: d.cfg.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(d.signer)},
		// No host key pin: the bridge is host-local, and every guest shares the
		// golden image's host key.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		_ = conn.Close()
		return nil, transportErr("ssh handshake", err)
	}
	// Clear the handshake deadline. Each op carries its own.
	_ = conn.SetDeadline(time.Time{})
	return &sshChannel{client: ssh.NewClient(cc, chans, reqs)}, nil
}

// sshChannel runs each command in its own session on one connection. Run is
// safe for concurrent use.
type sshChannel struct{ client *ssh.Client }

func (c *sshChannel) Close() error { return c.client.Close() }

// Run executes cmd with merged output to out and returns its exit code. ctx
// carries the deadline and kill. A deadline returns (124,
// context.DeadlineExceeded) like timeout(1), a cancellation (any,
// context.Canceled), and a failure before the command ran wraps ErrTransport.
func (c *sshChannel) Run(ctx context.Context, cmd Command, out io.Writer) (int, error) {
	session, err := c.client.NewSession()
	if err != nil {
		// A refused session leaves the connection and its other commands intact.
		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) {
			return 0, errors.Wrap(err, "opening session")
		}
		return 0, transportErr("opening session", err)
	}
	defer session.Close()
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, errors.Wrap(err, "run unit")
	}
	unit := "mvm-" + hex.EncodeToString(raw[:])
	// No PTY: a terminal would rewrite line endings and merge stderr.
	merged := &syncWriter{w: out}
	session.Stdout = merged
	session.Stderr = merged
	if len(cmd.Stdin) > 0 {
		session.Stdin = bytes.NewReader(cmd.Stdin)
	}
	if err := session.Start(remoteCommand(cmd, unit)); err != nil {
		return 0, transportErr("starting command", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		return classifyRemoteExit(err)
	case <-ctx.Done():
		// A signal request reaches only the session's process group and a channel
		// close only breaks the command's pipes, so kill the scope over a second
		// session, then close this one.
		c.kill(unit)
		_ = session.Close()
		select {
		case <-done:
		case <-time.After(killGrace):
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 124, context.DeadlineExceeded
		}
		return 0, ctx.Err()
	}
}

// killGrace bounds the wait for a killed command's session to end.
const killGrace = 5 * time.Second

// kill SIGKILLs every process in the run's scope, a cgroup, so it reaches
// the whole tree. Best effort, bounded by killGrace.
func (c *sshChannel) kill(unit string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := c.client.NewSession()
		if err != nil {
			return
		}
		defer s.Close()
		_ = s.Run("systemctl kill --signal=KILL " + unit + ".scope")
	}()
	select {
	case <-done:
	case <-time.After(killGrace):
	}
}

// classifyRemoteExit maps a session's end onto Run's (code, err) contract.
// A signal death takes the shell's 128+signal status.
func classifyRemoteExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		if sig := exitErr.Signal(); sig != "" {
			return 128 + signalNumbers[ssh.Signal(sig)], nil
		}
		return exitErr.ExitStatus(), nil
	}
	// The exit was never observed. Not safe to retry: the command ran.
	return 0, errors.Wrap(err, "remote command")
}

// signalNumbers maps ssh signal names to the guest's Linux numbers, whatever
// the broker's platform. Unknown names give a bare 128.
var signalNumbers = map[ssh.Signal]int{
	ssh.SIGABRT: 6, ssh.SIGALRM: 14, ssh.SIGFPE: 8, ssh.SIGHUP: 1,
	ssh.SIGILL: 4, ssh.SIGINT: 2, ssh.SIGKILL: 9, ssh.SIGPIPE: 13,
	ssh.SIGQUIT: 3, ssh.SIGSEGV: 11, ssh.SIGTERM: 15, ssh.SIGUSR1: 10,
	ssh.SIGUSR2: 12,
}
