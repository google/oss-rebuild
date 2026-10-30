// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/execx"
	"github.com/pkg/errors"
)

// stopGrace bounds QEMU's clean shutdown on TERM before it is killed.
const stopGrace = 5 * time.Second

// createOverlay creates a qcow2 overlay at path over a raw backing image.
func createOverlay(ctx context.Context, exec execx.CommandExecutor, bin, path, backing string) error {
	return run(ctx, exec, bin, "create", "-q", "-f", "qcow2", "-F", "raw", "-b", backing, path)
}

// ensureTap creates tap if needed and attaches it to bridge at mtu. The
// startup script owns the bridge.
func ensureTap(ctx context.Context, exec execx.CommandExecutor, tap, bridge string, mtu int) error {
	if err := run(ctx, exec, "ip", "link", "show", tap); err != nil {
		if err := run(ctx, exec, "ip", "tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
			return errors.Wrapf(err, "creating %s", tap)
		}
	}
	for _, args := range [][]string{
		{"link", "set", tap, "mtu", strconv.Itoa(mtu)},
		{"link", "set", tap, "master", bridge},
		{"link", "set", tap, "up"},
	} {
		if err := run(ctx, exec, "ip", args...); err != nil {
			return errors.Wrapf(err, "configuring %s", tap)
		}
	}
	return nil
}

// removeTap deletes tap with ip(8). A missing tap is not an error.
func removeTap(ctx context.Context, exec execx.CommandExecutor, tap string) error {
	if err := run(ctx, exec, "ip", "link", "show", tap); err != nil {
		return nil
	}
	return run(ctx, exec, "ip", "link", "del", tap)
}

// run executes a command, folding its output into any error.
func run(ctx context.Context, exec execx.CommandExecutor, name string, args ...string) error {
	var out bytes.Buffer
	if err := exec.Execute(ctx, execx.CommandOptions{Output: &out}, name, args...); err != nil {
		return errors.Wrapf(err, "%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return nil
}
