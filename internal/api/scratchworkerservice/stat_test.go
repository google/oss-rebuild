// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"context"
	"os"
	"testing"
)

func TestStat_FieldsAndDiskUsage(t *testing.T) {
	dir := t.TempDir()
	// Point socket probe at something that doesn't exist so we deterministically
	// see DockerSocketPresent = false.
	deps := &StatDeps{
		DockerSocketPath: "/tmp/this-socket-does-not-exist",
		DiskPaths:        []string{dir},
	}
	got, err := Stat(context.Background(), StatRequest{}, deps)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got.DockerSocketPresent {
		t.Errorf("DockerSocketPresent = true; want false (path absent)")
	}
	if got.DockerSocketPath != "/tmp/this-socket-does-not-exist" {
		t.Errorf("DockerSocketPath = %q; want %q", got.DockerSocketPath, "/tmp/this-socket-does-not-exist")
	}
	if len(got.Disks) != 1 {
		t.Fatalf("Disks len = %d; want 1", len(got.Disks))
	}
	if got.Disks[0].Path != dir || got.Disks[0].Total == 0 || got.Disks[0].Free == 0 {
		t.Errorf("Disks[0] = %+v; want non-zero Total/Free for %q", got.Disks[0], dir)
	}
}

func TestStat_DockerSocketPresent(t *testing.T) {
	// Create a UNIX-domain socket file (any file will do for os.Stat check).
	sock, err := os.CreateTemp(t.TempDir(), "fake-docker.sock")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	sock.Close()
	deps := &StatDeps{DockerSocketPath: sock.Name()}
	got, err := Stat(context.Background(), StatRequest{}, deps)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !got.DockerSocketPresent {
		t.Errorf("DockerSocketPresent = false; want true (path exists)")
	}
}
