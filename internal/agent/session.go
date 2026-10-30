// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"net/url"
	"path"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// Shell runs the model's diagnostic commands where the previous attempt's
// container and files are, and serves that attempt's build log.
type Shell interface {
	RunCommand(ctx context.Context, command string, timeoutSeconds int) (CommandResult, error)
	LastBuildLogs(ctx context.Context) ([]byte, error)
}

// CommandResult is one diagnostic command's outcome. Output is only what the
// runner read back. OutURI and TotalBytes locate and size the whole output.
type CommandResult struct {
	ExitCode   int
	Output     string
	ExecID     string
	OutURI     string
	TotalBytes int64
	StartedAt  time.Time
	FinishedAt time.Time
}

type AgentDeps struct {
	Chat   *llm.Chat
	ChatFn func(context.Context, []*llm.FunctionDefinition) (*llm.Chat, error) // Starts each iteration's fresh chat with the agent's tools.
	// Bucket for logs and rebuild artifact
	MetadataBucket     string
	LogsBucket         string
	GCSClient          *gcs.Client
	MaxTurns           int // Bounds each model exchange's tool uses (ChatOpts.MaxToolIterations)
	GenaiClient        *genai.Client
	RegistryClient     httpx.BasicClient // Upstream registry requests (e.g. adapt-mode registry refresh) via the session's identified egress path.
	Shell              Shell             // Backs the shell tools and read_logs_end.
	BuildEnv           rebuild.BuildEnv  // Must match the next attempt's executor. Empty means GCB.
	ShellHeadBytes     int               // Kept from the start of a shell result. Zero means the default.
	ShellTailBytes     int               // Kept from the end of a shell result. Zero means the default.
	SingleEnv          bool              // Runs the loop in the build container, without run_on_host.
	Model              string            // Gemini model id for auxiliary calls. Empty selects llm.GeminiPro.
	GitCache           *gitcache.Client  // When set, inference repo clones go through the git-cache.
	Retrier            ratex.Retrier     // Paces and retries the auxiliary model calls. Zero value calls once.
	CratesRegistryStub api.StubFn[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse]
	CratesCache        *cratesregistryservice.Cache // Inference's crates.io metadata.
}

type ProposeOpts struct {
	ChatUploadURL *url.URL // The path to which llm.Chat messages should be stored.
	Session       string   // The transcript's session, for tool-call records.
	Iteration     int      // The transcript's messages/<n> folder.
}

// upload is the transcript folder opts names, nil when nothing is stored.
func (o *ProposeOpts) upload() *url.URL {
	if o == nil {
		return nil
	}
	return o.ChatUploadURL
}

type Agent interface {
	Propose(context.Context, *ProposeOpts) (*schema.StrategyOneOf, error)
	// Canonicalize rewrites a verified build toward the form inference
	// produces (e.g. registry accesses confined to deps phase).
	Canonicalize(ctx context.Context, opts *ProposeOpts, working *schema.StrategyOneOf) (out *schema.StrategyOneOf, changed bool, err error)
	// Check judges a verified proposal against the content rule. A refusal
	// becomes the iteration's error message in place of a build result.
	Check(ctx context.Context, opts *ProposeOpts, proposal *schema.StrategyOneOf) (refusal string, err error)
	RecordIteration(*schema.AgentIteration)
	Usage() schema.TokenUsage // agent's cumulative LLM token consumption
}

type RunSessionReq struct {
	SessionID        string
	Target           rebuild.Target
	MaxIterations    int
	IterationBudget  time.Duration // No iteration starts past it. 0: unbounded.
	InitialIteration *schema.AgentIteration
}

type RunSessionDeps struct {
	Client        *genai.Client
	GCSClient     *gcs.Client
	IterationStub api.StubFn[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse]
	CompleteStub  api.StubFn[schema.AgentCompleteRequest, schema.AgentCompleteResponse]
	// TODO: Should these be asset stores?
	SessionsBucket     string
	MetadataBucket     string
	LogsBucket         string
	Retrier            ratex.Retrier     // Paces and retries model calls. Zero value calls the model once.
	RegistryClient     httpx.BasicClient // Upstream registry requests via the session's identified egress path.
	ScratchRunner      *ScratchRunner    // When set, each iteration builds on the scratch VM. Only verified successes reach the iteration API, as GCB confirmations.
	Model              string            // Gemini model id for the session's calls. Empty selects llm.GeminiPro.
	GitCache           *gitcache.Client  // When set, iteration inference repo clones go through the git-cache.
	CratesRegistryStub api.StubFn[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse]
	CratesCache        *cratesregistryservice.Cache // Inference's crates.io metadata.
	FreeForm           bool                         // Free-form mode. Needs ScratchRunner.
	Now                func() time.Time             // Budget clock. Nil uses time.Now.
}

// transcriptOpts names iteration iterNum's transcript directory, or nothing
// without a sessions bucket.
func transcriptOpts(deps RunSessionDeps, sessionID string, iterNum int) *ProposeOpts {
	opts := &ProposeOpts{Session: sessionID, Iteration: iterNum}
	if deps.SessionsBucket != "" {
		opts.ChatUploadURL = &url.URL{
			Scheme: "gs",
			Host:   deps.SessionsBucket,
			Path:   path.Join(sessionID, "messages", fmt.Sprintf("%d", iterNum)),
		}
	}
	return opts
}

// toolCallPath is a tool-call record's object name in the sessions bucket.
// Seq is zero-padded so a listing returns calls in order.
func toolCallPath(sessionID string, iteration, seq int, tool string) string {
	return fmt.Sprintf("%s/tools/%d/%04d-%s.json", sessionID, iteration, seq, tool)
}

func doIteration(ctx context.Context, sessionID string, iterNum int, agent Agent, deps RunSessionDeps) (*schema.AgentIteration, error) {
	opts := transcriptOpts(deps, sessionID, iterNum)
	// Snapshot the agent's cumulative token usage around Propose so this
	// iteration is charged only the tokens it consumed.
	beforeUsage := agent.Usage()
	s, err := agent.Propose(ctx, opts)
	if err != nil {
		return nil, errors.Wrap(err, "generating strategy")
	}
	iterUsage := agent.Usage().Sub(beforeUsage).OrNil()
	// Scratch-mode attempts run locally on the session's scratch VM and are
	// not recorded server-side (the scratch exec ledger is their trail).
	// Only a locally-verified success proceeds to the standard iteration
	// call below, which re-executes the strategy on GCB as the trusted
	// confirmation. That server-derived verdict becomes the recorded
	// iteration and, on success, the session's SuccessIteration.
	var verified time.Duration // the confirmed build's VM wall time
	if deps.ScratchRunner != nil {
		started := time.Now()
		status, result := deps.ScratchRunner.Run(ctx, fmt.Sprintf("iter-%d", iterNum), s)
		verified = time.Since(started)
		if status != schema.AgentIterationStatusSuccess {
			return localIteration(sessionID, iterNum, s, iterUsage, status, result), nil
		}
		// The build matched: judge how before a confirmation is spent on it.
		if refusal, cerr := agent.Check(ctx, opts, s); cerr != nil {
			log.Printf("Session %s Iteration %d: check failed (%v), confirming unchecked", sessionID, iterNum, cerr)
		} else if refusal != "" {
			log.Printf("Session %s Iteration %d: proposal refused", sessionID, iterNum)
			return localIteration(sessionID, iterNum, s, agent.Usage().Sub(beforeUsage).OrNil(), iterationStatusRefused, &schema.AgentBuildResult{ErrorMessage: refusal}), nil
		}
		log.Printf("Session %s Iteration %d: local build verified; confirming on GCB", sessionID, iterNum)
		if canon, changed, cerr := agent.Canonicalize(ctx, opts, s); cerr != nil {
			log.Printf("Session %s Iteration %d: canonicalize failed (%v), confirming original", sessionID, iterNum, cerr)
		} else if changed {
			// NOTE: Check the rewrite since it, too, is authored by the model.
			if refusal, cerr := agent.Check(ctx, opts, canon); cerr != nil || refusal != "" {
				log.Printf("Session %s Iteration %d: canonical form refused or unchecked (%v), confirming original", sessionID, iterNum, cerr)
			} else {
				started := time.Now()
				if st, _ := deps.ScratchRunner.Run(ctx, fmt.Sprintf("iter-%d-canon", iterNum), canon); st == schema.AgentIterationStatusSuccess {
					log.Printf("Session %s Iteration %d: canonical build re-verified, confirming canonical form", sessionID, iterNum)
					s, verified = canon, time.Since(started)
				} else {
					log.Printf("Session %s Iteration %d: canonical build did not re-verify (%s), confirming original", sessionID, iterNum, st)
				}
			}
		}
		// Charge the canonicalize turn to this iteration.
		iterUsage = agent.Usage().Sub(beforeUsage).OrNil()
		// The confirmation build produces no scratch exec traffic for its
		// (long) duration. Keep the VM visibly active so the idle reaper
		// doesn't tear it down mid-session.
		stop := deps.ScratchRunner.StartKeepAlive(ctx)
		defer stop()
	}
	// TODO: Should CreateIteration just return the Iteration object?
	// NOTE: The call takes no deadline of its own. The bound starts once the
	// build leaves the pool's queue, and the service's request timeout
	// already ends the call.
	resp, err := deps.IterationStub(ctx, schema.AgentCreateIterationRequest{
		SessionID:           sessionID,
		Strategy:            s,
		Usage:               iterUsage,
		BuildTimeoutSeconds: int(confirmationTimeout(verified).Seconds()),
	})
	if err != nil {
		return nil, errors.Wrap(err, "executing build")
	} else if resp == nil || resp.Iteration == nil {
		return nil, errors.New("iteration response is empty")
	}
	return resp.Iteration, nil
}

// A confirmation reruns the VM-verified build behind an image build and a tool
// fetch, on a start the pool can slow, so it gets a multiple of the VM's time
// plus an allowance. The median confirmation takes about 1.2 times the VM's
// time, and the factor leaves room for the slower ones.
const (
	confirmationFactor    = 2
	confirmationAllowance = 10 * time.Minute
)

// confirmationTimeout sizes the build from its VM wall time, or the service's
// maximum without a VM build.
func confirmationTimeout(verified time.Duration) time.Duration {
	if verified <= 0 {
		return schema.AgentBuildTimeoutMax
	}
	return min(confirmationFactor*verified+confirmationAllowance, schema.AgentBuildTimeoutMax)
}

// localIteration is an attempt that never reached the service: a failed
// scratch build or a refused proposal. It is never persisted.
func localIteration(sessionID string, iterNum int, s *schema.StrategyOneOf, usage *schema.TokenUsage, status string, result *schema.AgentBuildResult) *schema.AgentIteration {
	now := time.Now().UTC()
	return &schema.AgentIteration{
		SessionID: sessionID,
		Number:    iterNum,
		Strategy:  s,
		Status:    status,
		Result:    result,
		Usage:     usage,
		Created:   now,
		Updated:   now,
	}
}

// iterationStatusRefused marks a proposal the content review turned away. No
// build ran and the iteration never reaches the service, so the schema
// defines no such status.
const iterationStatusRefused = "REFUSED"

// maxRefusals ends a session that keeps proposing content instead of a build.
const maxRefusals = 3

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
		MaxOutputTokens: 65536, // max supported
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: "AUTO"},
		},
	}
	config = llm.WithSystemPrompt(config, genai.NewPartFromText("You are an expert at debugging rebuild failures"))
	a := NewDefaultAgent(req.Target, &AgentDeps{
		Chat:               nil,
		MetadataBucket:     deps.MetadataBucket,
		LogsBucket:         deps.LogsBucket,
		GCSClient:          deps.GCSClient,
		MaxTurns:           maxToolTurns,
		GenaiClient:        deps.Client,
		RegistryClient:     deps.RegistryClient,
		Model:              deps.Model,
		GitCache:           deps.GitCache,
		Retrier:            deps.Retrier,
		CratesRegistryStub: deps.CratesRegistryStub,
		CratesCache:        deps.CratesCache,
	})
	if deps.ScratchRunner != nil {
		a.deps.Shell = deps.ScratchRunner
		a.deps.BuildEnv = local.DockerRunBuildEnv()
		a.deps.SingleEnv = deps.ScratchRunner.SingleEnv
	}
	// Stamp the session's LLM token spend onto whatever completion we return.
	defer func() {
		if completeReq != nil {
			completeReq.Usage = a.Usage().OrNil()
		}
	}()
	a.deps.ChatFn = func(ctx context.Context, tools []*llm.FunctionDefinition) (*llm.Chat, error) {
		return llm.NewChat(ctx, deps.Client, cmp.Or(deps.Model, llm.GeminiPro), config, &llm.ChatOpts{Tools: tools, MaxToolIterations: a.deps.MaxTurns, Retrier: deps.Retrier})
	}
	if deps.FreeForm && deps.ScratchRunner != nil {
		// One chat per session, bounded by freeFormToolCalls.
		a.enableFreeForm(deps.ScratchRunner, func(ctx context.Context, system string, tools []*llm.FunctionDefinition) (*llm.Chat, error) {
			cfg := *config
			return llm.NewChat(ctx, deps.Client, cmp.Or(deps.Model, llm.GeminiPro), llm.WithSystemPrompt(&cfg, genai.NewPartFromText(system)), &llm.ChatOpts{Tools: tools, MaxToolIterations: freeFormToolCalls, Retrier: deps.Retrier})
		})
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
		if req.InitialIteration.Strategy != nil {
			// The seed skips Propose, so record its build here.
			a.uploadProposalRecord(ctx, transcriptOpts(deps, req.SessionID, iterNum), "0-proposal.json", req.InitialIteration.Strategy)
		}
	}
	return runIterations(ctx, req, iterNum, a, deps)
}

// A first build turn longer than slowIteration lifts the iteration budget.
// Such packages spend much of the budget per turn and reproduce late.
const slowIteration = 5 * time.Minute

// runIterations runs iterations until one is confirmed, the iteration cap or
// budget is spent, or ctx ends. The budget gates only an iteration's start and
// runs from the loop's start, lifted after a slow first build turn.
func runIterations(ctx context.Context, req RunSessionReq, iterNum int, a Agent, deps RunSessionDeps) *schema.AgentCompleteRequest {
	var transientErrs, buildAttempts, refusals int // tracks whether model made real progress or was throttled
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	var deadline time.Time
	if req.IterationBudget > 0 {
		deadline = now().Add(req.IterationBudget)
	}
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
		if !deadline.IsZero() && now().After(deadline) {
			return &schema.AgentCompleteRequest{
				StopReason: schema.AgentCompleteReasonFailed,
				Summary:    fmt.Sprintf("Iteration budget (%s) spent after %d builds", req.IterationBudget, buildAttempts),
			}
		}
		log.Printf("Session %s Iteration %d", req.SessionID, iterNum)
		iterStart := now()
		iteration, err := doIteration(ctx, req.SessionID, iterNum, a, deps)
		if err != nil {
			log.Printf("Doing iteration: %v", err)
			// A dead context fails every subsequent iteration the same way.
			if ctx.Err() != nil {
				return &schema.AgentCompleteRequest{
					StopReason: schema.AgentCompleteReasonError,
					Summary:    fmt.Sprintf("Session context ended: %v", context.Cause(ctx)),
				}
			}
			var gu gaveUp
			if errors.As(err, &gu) {
				return &schema.AgentCompleteRequest{StopReason: schema.AgentCompleteReasonGaveUp, Summary: "Gave up: " + gu.Reason, Closest: gu.Closest}
			}
			if llm.IsTransient(err) {
				transientErrs++
				continue
			}
			// Without a seed iteration or a build to learn from, the proposal
			// came from the deterministic inference so another attempt would also fail.
			if req.InitialIteration == nil && buildAttempts == 0 {
				return &schema.AgentCompleteRequest{
					StopReason: schema.AgentCompleteReasonFailed,
					Summary:    fmt.Sprintf("Inference failed: %v", err),
				}
			}
			continue
		}
		if iteration.Status == iterationStatusRefused {
			refusals++ // no build ran
		} else {
			buildAttempts++
			if took := now().Sub(iterStart); buildAttempts == 1 && !deadline.IsZero() && took > slowIteration {
				log.Printf("Session %s: first build turn took %s, lifting the iteration budget", req.SessionID, took.Round(time.Second))
				deadline = time.Time{}
			}
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
		case iterationStatusRefused:
			a.RecordIteration(iteration)
			if refusals >= maxRefusals {
				return &schema.AgentCompleteRequest{
					StopReason: schema.AgentCompleteReasonFailed,
					Summary:    fmt.Sprintf("Refused %d proposals that supplied artifact content instead of building it", refusals),
				}
			}
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
	// A session ended by cancellation must still report, or it holds its VM
	// until the reaper finds it.
	_, err := deps.CompleteStub(context.WithoutCancel(ctx), *completeReq)
	if err != nil {
		log.Printf("Failed to complete agent session: %v", err)
	} else {
		log.Println("Session cleanup completed")
	}
}
