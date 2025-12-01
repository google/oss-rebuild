// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package localagent

import (
	"context"
	"flag"
	"net/url"
	"time"

	"cloud.google.com/go/firestore"
	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/agent"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/genai"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config holds all configuration for the local-agent command.
type Config struct {
	Project        string
	AgentAPI       string
	MetadataBucket string
	LogsBucket     string
	Ecosystem      string
	Package        string
	Version        string
	Artifact       string
	RetrySession   string
	AgentIteration int
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.AgentAPI == "" {
		return errors.New("agent-api is required")
	}
	if c.MetadataBucket == "" {
		return errors.New("metadata-bucket is required")
	}
	if c.LogsBucket == "" {
		return errors.New("logs-bucket is required")
	}
	if c.Ecosystem == "" {
		return errors.New("ecosystem is required")
	}
	if c.Package == "" {
		return errors.New("package is required")
	}
	if c.Version == "" {
		return errors.New("version is required")
	}
	if c.Artifact == "" {
		return errors.New("artifact is required")
	}
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) {
	return &Deps{}, nil
}

// Handler contains the business logic for the local-agent command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	t := rebuild.Target{Ecosystem: rebuild.Ecosystem(cfg.Ecosystem), Package: cfg.Package, Version: cfg.Version, Artifact: cfg.Artifact}
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	aiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.Project,
		Location: "us-central1",
	})
	if err != nil {
		return nil, errors.Wrap(err, "making aiClient")
	}
	sessionUUID, err := uuid.NewV7()
	if err != nil {
		return nil, errors.Wrap(err, "making sessionID")
	}
	sessionID := sessionUUID.String()
	sessionTime := time.Unix(sessionUUID.Time().UnixTime())
	session := schema.AgentSession{
		ID:             sessionID,
		Target:         t,
		MaxIterations:  cfg.AgentIteration,
		TimeoutSeconds: 60 * 60, // 1 hr
		Context:        &schema.AgentContext{},
		// Because we're going to start running the session locally immediately, we can mark it as Running from the start.
		// This avoids needing to update the session record immediately after creation.
		Status: schema.AgentSessionStatusRunning,
		// There is no execution name, because we're going to run the agent in-process.
		ExecutionName: "",
		Created:       sessionTime,
		Updated:       sessionTime,
	}
	// Create session in Firestore
	err = fire.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		// NOTE: This would fail if the session record already exists.
		return t.Create(fire.Collection("agent_sessions").Doc(sessionID), session)
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return nil, errors.Errorf("agent session %s already exists", sessionID)
		}
		return nil, errors.Wrap(err, "creating agent session")
	}
	var retryInitialIter *schema.AgentIteration
	if cfg.RetrySession != "" {
		sessionDoc := fire.Collection("agent_sessions").Doc(cfg.RetrySession)
		iterQuery := sessionDoc.Collection("agent_iterations").
			Where("session_id", "==", cfg.RetrySession).
			Where("number", "==", 1).
			Limit(1)
		d, err := iterQuery.Documents(ctx).Next()
		if err != nil {
			return nil, errors.Wrap(err, "getting iteration to retry")
		}
		retryInitialIter = &schema.AgentIteration{}
		if err := d.DataTo(retryInitialIter); err != nil {
			return nil, errors.Wrap(err, "deserializing iteration data")
		}
		_, err = fire.Collection("agent_sessions").Doc(sessionID).
			Collection("agent_iterations").
			Doc(retryInitialIter.ID).
			Create(ctx, *retryInitialIter)
		if err != nil {
			return nil, errors.Wrap(err, "creating initial iteration")
		}
	}
	// Run agent locally
	client, err := oauth.AuthorizedUserIDClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating authorized HTTP client")
	}
	baseURL, err := url.Parse(cfg.AgentAPI)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to parse agent API URL")
	}
	gcsClient, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCS client")
	}
	// Create agent API client stubs
	iterationStub := api.Stub[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse](client, baseURL.JoinPath("agent/session/iteration"))
	completeStub := api.Stub[schema.AgentCompleteRequest, schema.AgentCompleteResponse](client, baseURL.JoinPath("agent/session/complete"))
	runDeps := agent.RunSessionDeps{
		Client:         aiClient,
		IterationStub:  iterationStub,
		CompleteStub:   completeStub,
		GCSClient:      gcsClient,
		SessionsBucket: "", // TODO: Add this once it's being used.
		MetadataBucket: cfg.MetadataBucket,
		LogsBucket:     cfg.LogsBucket,
	}
	req := agent.RunSessionReq{
		SessionID:        sessionID,
		Target:           t,
		MaxIterations:    cfg.AgentIteration,
		InitialIteration: retryInitialIter,
	}
	// TODO: Should RunSession return an error?
	agent.RunSession(ctx, req, runDeps)
	return &act.NoOutput{}, nil
}

// Command creates a new local-agent command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "local-agent --project <project> --agent-api <URI> --metadata-bucket <bucket> --ecosystem <ecosystem> --package <name> --version <version> --artifact <name> --logs-bucket <bucket> [--retry-session <session-id>] [--agent-iterations <max iterations>]",
		Short: "Run agent code locally",
		Args:  cobra.NoArgs,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			Handler,
		),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

// flagSet returns the command-line flags for the Config struct.
func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.StringVar(&cfg.AgentAPI, "agent-api", "", "Agent API endpoint URI")
	set.StringVar(&cfg.MetadataBucket, "metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	set.StringVar(&cfg.LogsBucket, "logs-bucket", "", "the gcs bucket where gcb logs are stored")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem")
	set.StringVar(&cfg.Package, "package", "", "the package name")
	set.StringVar(&cfg.Version, "version", "", "the version of the package")
	set.StringVar(&cfg.Artifact, "artifact", "", "the artifact name")
	set.StringVar(&cfg.RetrySession, "retry-session", "", "the session to retry")
	set.IntVar(&cfg.AgentIteration, "agent-iterations", 3, "maximum number of agent iterations before giving up")
	return set
}
