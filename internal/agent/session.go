// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type AgentLogic interface {
	ProposeStrategy(ctx context.Context, t rebuild.Target, prev *schema.AgentIteration) (*schema.StrategyOneOf, error)
}

type RunSessionReq struct {
	SessionID     string
	Target        rebuild.Target
	MaxIterations int
}

type RunSessionDeps struct {
	IterationStub api.StubT[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse]
	CompleteStub  api.StubT[schema.AgentCompleteRequest, schema.AgentCompleteResponse]
	Agent         AgentLogic
	// TODO: Should these be asset stores?
	SessionsBucket string
	MetadataBucket string
}

func doIteration(ctx context.Context, req RunSessionReq, iterNum int, prev *schema.AgentIteration, deps RunSessionDeps) (*schema.AgentIteration, error) {
	s, err := deps.Agent.ProposeStrategy(ctx, req.Target, prev)
	if err != nil {
		return nil, errors.Wrap(err, "generating strategy")
	}
	// TODO: Should CreateIteration just return the Iteration object?
	resp, err := deps.IterationStub(ctx, schema.AgentCreateIterationRequest{
		SessionID:       req.SessionID,
		IterationNumber: iterNum,
		Strategy:        s,
	})
	if err != nil {
		return nil, errors.Wrap(err, "executing build")
	} else if resp == nil || resp.Iteration == nil {
		return nil, errors.New("iteration response is empty")
	}
	return resp.Iteration, nil
}

func doSession(ctx context.Context, req RunSessionReq, deps RunSessionDeps) *schema.AgentCompleteRequest {
	if req.MaxIterations <= 0 {
		return &schema.AgentCompleteRequest{
			StopReason: schema.AgentCompleteReasonError,
			Summary:    fmt.Sprintf("MaximumIterations must be positive, provided %d", req.MaxIterations),
		}
	}
	var iteration, prev *schema.AgentIteration
	var iterNum int
	for {
		iterNum++
		if iterNum > req.MaxIterations {
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonFailed,
				Summary:    fmt.Sprintf("Maximum iterations (%d) reached", req.MaxIterations),
			}
		}
		log.Printf("Session %s Iteration %d", req.SessionID, iterNum)
		var err error
		iteration, err = doIteration(ctx, req, iterNum, prev, deps)
		if err != nil {
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonError,
				Summary:    errors.Wrap(err, "doing iteration").Error(),
			}
		}
		switch iteration.Status {
		case schema.AgentIterationStatusSuccess:
			return &schema.AgentCompleteRequest{
				StopReason:         schema.AgentCompleteReasonSuccess,
				Summary:            "Build successful",
				SuccessIterationID: iteration.ID,
			}
		case schema.AgentIterationStatusFailed:
			// If the iteration failed, use that as input to the next iteration of the agent.
			// This should allow it to refine and improve the rebuild strategy.
			prev = iteration
			continue
		case schema.AgentIterationStatusError:
			// Don't update prev, we want the last non-error iteration as the basis for the next guess.
			continue
		default:
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonError,
				Summary:    fmt.Sprintf("Unpexcted iteration status: %s", iteration.Status),
			}
		}
	}
}

func RunSession(ctx context.Context, req RunSessionReq, deps RunSessionDeps) {
	completeReq := doSession(ctx, req, deps)
	completeReq.SessionID = req.SessionID
	_, err := deps.CompleteStub(ctx, *completeReq)
	if err != nil {
		log.Printf("Failed to complete agent session: %v", err)
	} else {
		log.Println("Session cleanup completed")
	}
}
