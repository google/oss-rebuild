// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import "github.com/pkg/errors"

// Routes the broker serves. All but RouteHealthz are act handlers.
const (
	RouteAdmit       = "/guest/admit"
	RouteDelete      = "/guest/delete"
	RouteList        = "/guests"
	RouteGuestHealth = "/guest/healthz"
	RouteHealthz     = "/healthz"
)

// AdmitRequest boots a guest for a session. Idempotent on ScratchID.
type AdmitRequest struct {
	ScratchID string `form:"scratch_id,required"`
}

// Validate implements act.Input.
func (r AdmitRequest) Validate() error { return validateScratchID(r.ScratchID) }

// GuestRequest names a session's guest.
type GuestRequest struct {
	ScratchID string `form:"scratch_id,required"`
}

// Validate implements act.Input.
func (r GuestRequest) Validate() error { return validateScratchID(r.ScratchID) }

// ListRequest asks for the host's guests.
type ListRequest struct{}

// Validate implements act.Input.
func (ListRequest) Validate() error { return nil }

// validateScratchID refuses, rather than sanitizes, any id that could
// escape the per-session directories it names.
func validateScratchID(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("scratch_id must be 1 to 128 characters")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return errors.Errorf("scratch_id %q is not a plain identifier", id)
		}
	}
	return nil
}

// HostAudience is the ID-token audience the broker verifies. It is per
// host: sessions are told apart by scratch id.
func HostAudience(hostName string) string { return "https://builder/" + hostName }
