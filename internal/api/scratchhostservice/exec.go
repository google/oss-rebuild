// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"encoding/base64"
	"io"
	"iter"
	"time"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// StartRequest starts an op. The caller mints OpID. TimeoutSeconds must be
// positive.
type StartRequest struct {
	OpID           string            `form:"op_id,required"`
	ScratchID      string            `form:"scratch_id,required"`
	Cmd            []string          `form:"cmd,required"`
	Cwd            string            `form:"cwd"`
	Env            map[string]string `form:"env" act:"nolog"` // may hold sensitive values, e.g. creds
	StdinB64       string            `form:"stdin_b64"`
	TimeoutSeconds int               `form:"timeout_seconds"`
}

// Validate implements act.Input.
func (r StartRequest) Validate() error {
	if r.OpID == "" {
		return errors.New("op_id is required")
	}
	if err := validateScratchID(r.ScratchID); err != nil {
		return err
	}
	if len(r.Cmd) == 0 {
		return errors.New("cmd is required")
	}
	if r.TimeoutSeconds <= 0 {
		return errors.New("timeout_seconds must be > 0")
	}
	return nil
}

// OpRequest names an op.
type OpRequest struct {
	ID string `form:"id,required"`
}

// Validate implements act.Input.
func (r OpRequest) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	return nil
}

// OutputRequest streams an op's output from Offset.
type OutputRequest struct {
	ID     string `form:"id,required"`
	Offset int64  `form:"offset"`
}

// Validate implements act.Input.
func (r OutputRequest) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	if r.Offset < 0 {
		return errors.New("offset must be >= 0")
	}
	return nil
}

// ExecStart starts the op in the session's guest. The other exec handlers
// read the ledger, which outlives the guest.
func ExecStart(_ context.Context, req StartRequest, deps *GuestDeps) (*act.NoOutput, error) {
	g, err := deps.Host.liveGuest(time.Now().UTC(), req.ScratchID)
	if err != nil {
		return nil, api.AsStatus(codes.NotFound, errors.Wrapf(err, "guest for %s", req.ScratchID))
	}
	ledger, ok := deps.Host.ledger(req.ScratchID)
	if !ok {
		return nil, api.AsStatus(codes.NotFound, errors.Errorf("no session %s on this host", req.ScratchID))
	}
	stdin, err := base64.StdEncoding.DecodeString(req.StdinB64)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "decode stdin"))
	}
	cmd := Command{Argv: req.Cmd, Cwd: req.Cwd, Env: req.Env, Stdin: stdin}
	if cmd.Cwd == "" {
		cmd.Cwd = deps.Config.Guest.Workdir
	}
	channels := deps.ChannelCache
	err = ledger.Start(req.OpID, req.TimeoutSeconds, func(ctx context.Context, out io.Writer) (int, error) {
		return channels.run(ctx, g.IP, cmd, out)
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "starting op"))
	}
	return &act.NoOutput{}, nil
}

// Status returns an op's ExecStatus.
func Status(_ context.Context, req OpRequest, deps *GuestDeps) (*ExecStatus, error) {
	if ledger, ok := deps.Host.ledgerFor(req.ID); ok {
		if st, ok := ledger.Status(req.ID); ok {
			return &st, nil
		}
	}
	return nil, api.AsStatus(codes.NotFound, errors.Errorf("unknown op %q", req.ID))
}

// Kill cancels a running op. Idempotent on a finished op.
func Kill(_ context.Context, req OpRequest, deps *GuestDeps) (*act.NoOutput, error) {
	ledger, ok := deps.Host.ledgerFor(req.ID)
	if !ok {
		return nil, api.AsStatus(codes.NotFound, errors.Errorf("unknown op %q", req.ID))
	}
	if err := ledger.Kill(req.ID); err != nil {
		return nil, api.AsStatus(codes.NotFound, err)
	}
	return &act.NoOutput{}, nil
}

// Output streams an op's output from req.Offset, up to the capture's size
// on entry. An unknown op is a pre-stream NotFound.
func Output(ctx context.Context, req OutputRequest, deps *GuestDeps) iter.Seq2[*OutputFrame, error] {
	ledger, ok := deps.Host.ledgerFor(req.ID)
	if !ok {
		return func(yield func(*OutputFrame, error) bool) {
			yield(nil, api.AsStatus(codes.NotFound, errors.Errorf("unknown op %q", req.ID)))
		}
	}
	return ledger.Frames(ctx, req.ID, req.Offset)
}
