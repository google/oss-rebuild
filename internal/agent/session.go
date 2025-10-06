// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

type AgentDeps struct {
	Chat *llm.Chat
	// Bucket for logs and rebuild artifact
	MetadataBucket string
	LogsBucket     string
	GCSClient      *gcs.Client
	MaxTurns       int
}

type ProposeOpts struct {
	ChatUploadURL *url.URL // The path to which llm.Chat messages should be stored.
}

type Agent interface {
	Propose(context.Context, *ProposeOpts) (*schema.StrategyOneOf, error)
	RecordIteration(*schema.AgentIteration)
}

type RunSessionReq struct {
	SessionID        string
	Target           rebuild.Target
	MaxIterations    int
	InitialIteration *schema.AgentIteration
}

type RunSessionDeps struct {
	Client        *genai.Client
	GCSClient     *gcs.Client
	IterationStub api.StubT[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse]
	CompleteStub  api.StubT[schema.AgentCompleteRequest, schema.AgentCompleteResponse]
	// TODO: Should these be asset stores?
	SessionsBucket string
	MetadataBucket string
	LogsBucket     string
}

func doIteration(ctx context.Context, sessionID string, iterNum int, agent Agent, deps RunSessionDeps) (*schema.AgentIteration, error) {
	opts := &ProposeOpts{}
	if deps.SessionsBucket != "" {
		opts.ChatUploadURL = &url.URL{
			Scheme: "gs",
			Host:   deps.SessionsBucket,
			Path:   path.Join(sessionID, "messages", fmt.Sprintf("%d", iterNum)),
		}
	}
	s, err := agent.Propose(ctx, opts)
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
	config := &genai.GenerateContentConfig{
		Temperature:     genai.Ptr[float32](.1),
		MaxOutputTokens: 16000,
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: "AUTO"},
		},
	}
	config = llm.WithSystemPrompt(config, genai.NewPartFromText("You are an expert at debugging rebuild failures"))
	a := NewDefaultAgent(req.Target, &AgentDeps{
		Chat:           nil,
		MetadataBucket: deps.MetadataBucket,
		LogsBucket:     deps.LogsBucket,
		GCSClient:      deps.GCSClient,
		MaxTurns:       10,
	})
	var err error
	a.deps.Chat, err = llm.NewChat(ctx, deps.Client, llm.GeminiPro, config, &llm.ChatOpts{Tools: a.getTools()})
	if err != nil {
		return &schema.AgentCompleteRequest{
			StopReason: schema.AgentCompleteReasonError,
			Summary:    fmt.Sprintf("Initializing agent: %v", err),
		}
	}
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
