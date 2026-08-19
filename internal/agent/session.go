// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/llm"
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
	GenaiClient    *genai.Client
	RegistryClient httpx.BasicClient // Upstream registry requests (e.g. adapt-mode registry refresh) via the session's identified egress path.
	ScratchRunner  *ScratchRunner    // When set, iteration builds run on a scratch VM with build logs read from exec output.
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
	IterationStub api.StubFn[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse]
	CompleteStub  api.StubFn[schema.AgentCompleteRequest, schema.AgentCompleteResponse]
	// TODO: Should these be asset stores?
	SessionsBucket string
	MetadataBucket string
	LogsBucket     string
	Retrier        ratex.Retrier     // Paces and retries model calls. Zero value calls the model once.
	RegistryClient httpx.BasicClient // Upstream registry requests via the session's identified egress path.
	ScratchRunner  *ScratchRunner    // When set, each iteration builds on the scratch VM. Only verified successes reach the iteration API, as GCB confirmations.
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
	// Scratch-mode attempts run locally on the session's scratch VM and are
	// not recorded server-side (the scratch exec ledger is their trail).
	// Only a locally-verified success proceeds to the standard iteration
	// call below, which re-executes the strategy on GCB as the trusted
	// confirmation. That server-derived verdict becomes the recorded
	// iteration and, on success, the session's SuccessIteration.
	if deps.ScratchRunner != nil {
		status, result := deps.ScratchRunner.Run(ctx, fmt.Sprintf("iter-%d", iterNum), s)
		if status != schema.AgentIterationStatusSuccess {
			now := time.Now().UTC()
			return &schema.AgentIteration{
				SessionID: sessionID,
				Number:    iterNum,
				Strategy:  s,
				Status:    status,
				Result:    result,
				Created:   now,
				Updated:   now,
			}, nil
		}
		log.Printf("Session %s Iteration %d: local build verified; confirming on GCB", sessionID, iterNum)
		// The confirmation build produces no scratch exec traffic for its
		// (long) duration. Keep the VM visibly active so the idle reaper
		// doesn't tear it down mid-session.
		stop := deps.ScratchRunner.StartKeepAlive(ctx)
		defer stop()
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

func doSession(ctx context.Context, req RunSessionReq, deps RunSessionDeps) (completeReq *schema.AgentCompleteRequest) {
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
		GenaiClient:    deps.Client,
		RegistryClient: deps.RegistryClient,
		ScratchRunner:  deps.ScratchRunner,
	})
	var err error
	a.deps.Chat, err = llm.NewChat(ctx, deps.Client, llm.GeminiPro, config, &llm.ChatOpts{Tools: a.getTools(), Retrier: deps.Retrier})
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
	// Stamp the session's LLM token spend onto whatever completion we return.
	defer func() {
		if completeReq != nil {
			completeReq.TotalTokens = a.TotalTokens()
		}
	}()
	var transientErrs, buildAttempts int // tracks whether model made real progress or was throttled
	for {
		iterNum++
		if iterNum > req.MaxIterations {
			reason, summary := schema.AgentCompleteReasonFailed, fmt.Sprintf("Maximum iterations (%d) reached", req.MaxIterations)
			if buildAttempts == 0 && transientErrs > 0 {
				// No progress was made on the package.
				reason = schema.AgentCompleteReasonThrottled
				summary = fmt.Sprintf("Session throttled: %d transient LLM failures with no completed build attempts", transientErrs)
			}
			return &schema.AgentCompleteRequest{StopReason: reason, Summary: summary}
		}
		log.Printf("Session %s Iteration %d", req.SessionID, iterNum)
		iteration, err := doIteration(ctx, req.SessionID, iterNum, a, deps)
		if err != nil {
			log.Printf("Doing iteration: %v", err)
			// A dead context fails every subsequent iteration the same way.
			if ctx.Err() != nil {
				return &schema.AgentCompleteRequest{
					StopReason: schema.AgentCompleteReasonError,
					Summary:    fmt.Sprintf("Session context ended: %v", ctx.Err()),
				}
			}
			if llm.IsTransient(err) {
				transientErrs++
			}
			continue
		}
		buildAttempts++
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
