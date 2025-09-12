// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tmux

import (
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/pkg/errors"
)

// Start launches the cmd in a new tmux pain, but does not wait for it to complete.
func Start(cmd string) error {
	c := exec.Command("tmux", "new-window", cmd)
	if _, err := c.Output(); err != nil {
		log.Println("Maybe you're not running inside a tmux session?")
		return errors.Wrap(err, "opening tmux window")
	}
	return nil
}

// Wait launches the cmd in a new tmux pain and waits for it to complete before returning.
func Wait(cmd string) error {
	// Send a "tmux wait -S" signal once the cmd is complete.
	done := fmt.Sprintf("done%d", time.Now().UnixNano())
	c := exec.Command("tmux", "new-window", fmt.Sprintf("%s; tmux wait -S %s", cmd, done))
	if _, err := c.Output(); err != nil {
		log.Println("Maybe you're not running inside a tmux session?")
		return errors.Wrap(err, "opening tmux window")
	}
	// Wait to receive the tmux signal.
	if _, err := exec.Command("tmux", "wait", done).Output(); err != nil {
		return errors.Wrap(err, "failed to wait for tmux signal")
	}
	return nil
}
