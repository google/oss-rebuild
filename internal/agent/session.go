// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
	IterationStub func(context.Context, schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error)
	CompleteStub  func(context.Context, schema.AgentCompleteRequest) (*schema.AgentCompleteResponse, error)
	Agent         AgentLogic
	// TODO: Should these be asset stores?
	SessionsBucket string
	MetadataBucket string
}

func RunSession(ctx context.Context, req RunSessionReq, deps RunSessionDeps) {
	log.Printf("Agent running for session %s, target: %+v", req.SessionID, req.Target)
	// Defer a call to complete the session. We'll keep completeReq updated as we go.
	completeReq := schema.AgentCompleteRequest{
		SessionID: req.SessionID,
	}
	defer func() {
		// If completeReq wasn't updated with a reason, add a generic one.
		if completeReq.StopReason == "" && completeReq.Summary == "" {
			completeReq.StopReason = schema.AgentCompleteReasonError
			completeReq.Summary = "Agent completed with unknown status"
		}
		_, err := deps.CompleteStub(ctx, completeReq)
		if err != nil {
			log.Fatalf("Failed to complete agent session: %v", err)
		}
		log.Println("Session cleanup completed")
	}()
	var iteration *schema.AgentIteration
	var iterNum int
	for range req.MaxIterations {
		iterNum++
		log.Printf("Iteration %d", iterNum)
		log.Println("Allowing the agent to propose a strategy")
		s, err := deps.Agent.ProposeStrategy(ctx, req.Target, iteration)
		if err != nil {
			log.Println(errors.Wrap(err, "generating strategy"))
			completeReq.StopReason = schema.AgentCompleteReasonError
			completeReq.Summary = errors.Wrap(err, "generating strategy").Error()
			break
		}
		// Log strategy for debugging
		{
			strat, err := json.Marshal(s)
			if err != nil {
				log.Printf("Trying to log debug strategy: %v", err)
			} else {
				log.Printf("Strategy generated: %s", string(strat))
			}
		}
		log.Println("Executing the iteration")
		resp, err := deps.IterationStub(ctx, schema.AgentCreateIterationRequest{
			SessionID:       req.SessionID,
			IterationNumber: iterNum,
			Strategy:        s,
		})
		if err != nil {
			log.Println(errors.Wrap(err, "executing build"))
			completeReq.StopReason = schema.AgentCompleteReasonError
			completeReq.Summary = errors.Wrap(err, "executing build").Error()
			break
		} else if resp == nil || resp.Iteration == nil {
			log.Println("Iteration response is nil or iteration is nil")
			completeReq.StopReason = schema.AgentCompleteReasonError
			completeReq.Summary = "Iteration response is nil or iteration is nil"
			break
		}
		// TODO: Should CreateIteration just return the Iteration object?
		iteration = resp.Iteration
		if iteration.Status == schema.AgentIterationStatusSuccess {
			log.Println("Build successful!")
			completeReq.StopReason = schema.AgentCompleteReasonSuccess
			completeReq.Summary = "Build successful"
			completeReq.SuccessIterationID = iteration.ID
			break
		} else if iteration.Status == schema.AgentIterationStatusFailed {
			if iteration.Result == nil {
				completeReq.StopReason = schema.AgentCompleteReasonError
				completeReq.Summary = "Build failed but results is nil"
				break
			}
			log.Printf("Build failed with status %s: %s", iteration.Status, iteration.Result.ErrorMessage)
		} else if iteration.Status == schema.AgentIterationStatusError {
			log.Printf("Iteration completed with unexpected status %s", iteration.Status)
		}
	}
	if iterNum == req.MaxIterations && completeReq.StopReason == "" {
		completeReq.StopReason = schema.AgentCompleteReasonFailed
		completeReq.Summary = fmt.Sprintf("Maximum iterations (%d) reached", req.MaxIterations)
	}
	var lastIterationID string
	if iteration != nil {
		lastIterationID = iteration.ID
	}
	log.Printf("%s: Agent session %s finished after %d iterations, final iterationID: %s", completeReq.StopReason, req.SessionID, iterNum, lastIterationID)
}
