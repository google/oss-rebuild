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
	MachineClass MachineClass `json:"machine_class,omitempty" firestore:"machine_class,omitempty"`
	VMName       string       `json:"vm_name,omitempty" firestore:"vm_name,omitempty"`
	InternalIP   string       `json:"internal_ip,omitempty" firestore:"internal_ip,omitempty"`
	Zone         string       `json:"zone,omitempty" firestore:"zone,omitempty"`
	State        ScratchState `json:"state,omitempty" firestore:"state,omitempty"`
	Created      time.Time    `json:"created,omitzero" firestore:"created,omitempty"`
	Updated      time.Time    `json:"updated,omitzero" firestore:"updated,omitempty"`
	LastUsed     time.Time    `json:"last_used,omitzero" firestore:"last_used,omitempty"`
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
