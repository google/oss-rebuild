// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"
)

// ScratchState is the lifecycle state of a scratch build environment.
type ScratchState string

const (
	ScratchStarting ScratchState = "starting"
	ScratchReady    ScratchState = "ready"
	ScratchDeleting ScratchState = "deleting"
	ScratchDeleted  ScratchState = "deleted"
)

// MachineClass selects the VM size for a scratch.
type MachineClass string

const (
	MachineClassStandard MachineClass = "standard"
	MachineClassJumbo    MachineClass = "jumbo"
)

// Scratch is a per-build agent scratch environment. It binds a VM to an
// agent for maintaining build-local state (Docker daemon storage, working
// directory, build caches) on the VM's local SSD.
type Scratch struct {
	ID           string       `json:"id,omitempty" firestore:"id,omitempty"`
	BuildID      string       `json:"build_id,omitempty" firestore:"build_id,omitempty"`
	ObliviousID  string       `json:"oblivious_id,omitempty" firestore:"oblivious_id,omitempty"`
	MachineClass MachineClass `json:"machine_class,omitempty" firestore:"machine_class,omitempty"`
	VMName       string       `json:"vm_name,omitempty" firestore:"vm_name,omitempty"`
	InternalIP   string       `json:"internal_ip,omitempty" firestore:"internal_ip,omitempty"`
	Zone         string       `json:"zone,omitempty" firestore:"zone,omitempty"`
	State        ScratchState `json:"state,omitempty" firestore:"state,omitempty"`
	Created      time.Time    `json:"created,omitzero" firestore:"created,omitempty"`
	Updated      time.Time    `json:"updated,omitzero" firestore:"updated,omitempty"`
	// LastUsed is the last agent interaction: exec dispatch or an
	// agent poll observing exec completion. The reaper treats Ready
	// scratches with stale LastUsed and no in-deadline pending execs
	// as idle.
	LastUsed time.Time `json:"last_used,omitzero" firestore:"last_used,omitempty"`
}

// ScratchCreateRequest is the input to /scratch/create.
type ScratchCreateRequest struct {
	BuildID      string       `form:"build_id,required"`
	MachineClass MachineClass `form:"machine_class,required"`
}

// Validate implements act.Input.
func (r ScratchCreateRequest) Validate() error {
	if r.BuildID == "" {
		return errors.New("build_id is required")
	}
	if r.MachineClass == "" {
		return errors.New("machine_class is required")
	}
	return nil
}

// ScratchGetRequest is the input to /scratch/get.
type ScratchGetRequest struct {
	ScratchID string `form:"scratch_id,required"`
}

// Validate implements act.Input.
func (r ScratchGetRequest) Validate() error {
	if r.ScratchID == "" {
		return errors.New("scratch_id is required")
	}
	return nil
}

// ScratchDeleteRequest is the input to /scratch/delete.
type ScratchDeleteRequest struct {
	ScratchID string `form:"scratch_id,required"`
}

// Validate implements act.Input.
func (r ScratchDeleteRequest) Validate() error {
	if r.ScratchID == "" {
		return errors.New("scratch_id is required")
	}
	return nil
}

// ScratchDeleteResponse is returned by /scratch/delete.
type ScratchDeleteResponse struct {
	ScratchID string       `json:"scratch_id"`
	State     ScratchState `json:"state"`
}

// ScratchExecState is the lifecycle state of one exec on a scratch.
//
// Pending is the only non-terminal value; transitions to any other state
// are one-way and the record becomes immutable afterwards.
type ScratchExecState string

const (
	ScratchExecPending   ScratchExecState = "pending"
	ScratchExecCompleted ScratchExecState = "completed" // worker observed the command's exit (any code)
	ScratchExecTimedOut  ScratchExecState = "timed_out" // worker- or broker-enforced deadline
	ScratchExecLost      ScratchExecState = "lost"      // dispatch failed, scratch gone, or sync failure
)

// Status is a structured diagnostic for a terminal failure. It is
// modeled after google.rpc.Status; Code is a gRPC status code carried
// as an int (matching longrunning.OperationError.Code so the projection
// is a direct copy).
//
// The schema package deliberately does not import google.golang.org/grpc/codes
// to keep the type free of RPC-layer dependencies. Callers should construct
// Status values with int(codes.X). Common values emitted by the broker today
// are codes.DeadlineExceeded, codes.Unavailable, and codes.Internal. Readers
// should treat unknown codes as equivalent to codes.Internal.
type Status struct {
	Code    int    `json:"code" firestore:"code"`
	Message string `json:"message,omitempty" firestore:"message,omitempty"`
}

// ScratchExec is the stored form of one exec on a scratch. It is the
// canonical source of truth; the wire-facing Operation[ScratchExecResult]
// is derived from it (see agentapiservice.ProjectScratchExec).
//
// stdout and stderr are merged on the worker into a single interleaved
// stream published at OutURI; the broker writes that object on each
// sync poll. OutURI is set at creation and updated incrementally during
// execution and may hold partial data in any State, including Failed.
//
// Field validity by State:
//   - Pending:   ExitCode 0; Error nil; OutURI may be partial.
//   - Completed: ExitCode is the observed command exit; Error nil; OutURI complete.
//   - TimedOut:  ExitCode is the kill exit (typically 124); Error carries detail; OutURI may be partial.
//   - Lost:      Error non-nil; ExitCode 0 (no exit observed); OutURI may be partial.
type ScratchExec struct {
	ID        string   `json:"id" firestore:"id"`
	ScratchID string   `json:"scratch_id,omitempty" firestore:"scratch_id,omitempty"`
	Cmd       []string `json:"cmd,omitempty" firestore:"cmd,omitempty"`
	Cwd       string   `json:"cwd,omitempty" firestore:"cwd,omitempty"`
	// TimeoutSeconds is the worker-enforced execution bound, stamped by the
	// broker at create (request value, or the configured default when the
	// request omits it). The reaper derives each op's hard deadline from it.
	// Zero means unbounded: the reaper warns and leaves the scratch up.
	TimeoutSeconds int              `json:"timeout_seconds,omitempty" firestore:"timeout_seconds,omitempty"`
	OutURI         string           `json:"out_uri,omitempty" firestore:"out_uri,omitempty"`
	StartedAt      time.Time        `json:"started_at,omitzero" firestore:"started_at,omitempty"`
	FinishedAt     time.Time        `json:"finished_at,omitzero" firestore:"finished_at,omitempty"`
	State          ScratchExecState `json:"state" firestore:"state"`
	ExitCode       int              `json:"exit_code,omitempty" firestore:"exit_code,omitempty"`
	Error          *Status          `json:"error,omitempty" firestore:"error,omitempty"`
}

// ScratchExecResult is the API payload inside derived from ScratchExec.
type ScratchExecResult struct {
	ScratchID  string    `json:"scratch_id"`
	ExitCode   int       `json:"exit_code"`
	OutURI     string    `json:"out_uri,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

// ScratchExecRequest is the agent's input to /scratch/exec/op/create.
type ScratchExecRequest struct {
	ScratchID      string            `form:"scratch_id,required"`
	Cmd            []string          `form:"cmd,required"`
	Cwd            string            `form:"cwd"`
	Env            map[string]string `form:"env"`
	StdinB64       string            `form:"stdin_b64"`
	TimeoutSeconds int               `form:"timeout_seconds"`
	WaitSeconds    int               `form:"wait_seconds"`
}

// Validate implements act.Input.
func (r ScratchExecRequest) Validate() error {
	if r.ScratchID == "" {
		return errors.New("scratch_id is required")
	}
	if len(r.Cmd) == 0 {
		return errors.New("cmd is required")
	}
	if r.TimeoutSeconds < 0 {
		return errors.New("timeout_seconds must be >= 0")
	}
	if r.WaitSeconds < 0 {
		return errors.New("wait_seconds must be >= 0")
	}
	return nil
}
