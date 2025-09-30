// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/firebase/genkit/go/genkit"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type AgentDeps struct {
	Genkit *genkit.Genkit
	// Bucket for logs and rebuild artifact
	MetadataBucket string
	LogsBucket     string
	MaxTurns       int
}

type Agent interface {
	Propose(context.Context) (*schema.StrategyOneOf, error)
	RecordIteration(*schema.AgentIteration)
}

type RunSessionReq struct {
	SessionID        string
	Target           rebuild.Target
	MaxIterations    int
	InitialIteration *schema.AgentIteration
}

type RunSessionDeps struct {
	Genkit        *genkit.Genkit
	IterationStub api.StubT[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse]
	CompleteStub  api.StubT[schema.AgentCompleteRequest, schema.AgentCompleteResponse]
	// TODO: Should these be asset stores?
	SessionsBucket string
	MetadataBucket string
	LogsBucket     string
}

func doIteration(ctx context.Context, sessionID string, iterNum int, agent Agent, deps RunSessionDeps) (*schema.AgentIteration, error) {
	s, err := agent.Propose(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "generating strategy")
	}
	// TODO: Should CreateIteration just return the Iteration object?
	resp, err := deps.IterationStub(ctx, schema.AgentCreateIterationRequest{
		SessionID:       sessionID,
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
	var iterNum int
	a := NewDefaultAgent(req.Target, &AgentDeps{
		Genkit:         deps.Genkit,
		MetadataBucket: deps.MetadataBucket,
		LogsBucket:     deps.LogsBucket,
		MaxTurns:       10,
	})
	if req.InitialIteration != nil {
		err := a.InitializeFromIteration(ctx, req.InitialIteration)
		if err != nil {
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonError,
				Summary:    fmt.Sprintf("Initializing agent: %v", err),
			}
		}
		iterNum = 1
	}
	for {
		iterNum++
		if iterNum > req.MaxIterations {
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonFailed,
				Summary:    fmt.Sprintf("Maximum iterations (%d) reached", req.MaxIterations),
			}
		}
		log.Printf("Session %s Iteration %d", req.SessionID, iterNum)
		iteration, err := doIteration(ctx, req.SessionID, iterNum, a, deps)
		if err != nil {
			log.Printf("Doing iteration: %v", err)
			continue
		}
		log.Printf("%#v", iteration)
		if iteration != nil && iteration.Result != nil && !iteration.Result.BuildSuccess {
			log.Printf("Build failed: %s", iteration.Result.ErrorMessage)
		}
		switch iteration.Status {
		case schema.AgentIterationStatusSuccess:
			return &schema.AgentCompleteRequest{
				StopReason:         schema.AgentCompleteReasonSuccess,
				Summary:            "Build successful",
				SuccessIterationID: iteration.ID,
			}
		case schema.AgentIterationStatusFailed:
			a.RecordIteration(iteration)
			continue
		case schema.AgentIterationStatusError:
			// Don't record the iteration, we want the last non-error iteration as the basis for the next guess.
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
	if completeReq.StopReason == schema.AgentCompleteReasonError {
		log.Printf("Session error: %s", completeReq.Summary)
	}
	completeReq.SessionID = req.SessionID
	_, err := deps.CompleteStub(ctx, *completeReq)
	if err != nil {
		log.Printf("Failed to complete agent session: %v", err)
	} else {
		log.Println("Session cleanup completed")
	}
}
