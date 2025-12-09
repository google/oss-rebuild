// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runagent

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config holds all configuration for the run-agent command.
type Config struct {
	Project         string
	API             string
	Ecosystem       string
	Package         string
	Version         string
	Artifact        string
	AgentIterations int
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
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

// Handler contains the business logic for the run-agent command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	apiURL, err := url.Parse(cfg.API)
	if err != nil {
		return nil, errors.Wrap(err, "parsing API endpoint")
	}
	t := rebuild.Target{Ecosystem: rebuild.Ecosystem(cfg.Ecosystem), Package: cfg.Package, Version: cfg.Version, Artifact: cfg.Artifact}
	var client *http.Client
	{
		if strings.Contains(apiURL.Host, "run.app") {
			// If the api is on Cloud Run, we need to use an authorized client.
			apiURL.Scheme = "https"
			client, err = oauth.AuthorizedUserIDClient(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "creating authorized HTTP client")
			}
		} else {
			client = http.DefaultClient
		}
	}
	stub := api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent"))
	resp, err := stub(ctx, schema.AgentCreateRequest{
		Target:        t,
		MaxIterations: cfg.AgentIterations,
	})
	if err != nil {
		return nil, errors.Wrap(err, "running attest")
	}
	log.Printf("Successfully started session %s", resp.SessionID)
	sessionDoc := fire.Collection("agent_sessions").Doc(resp.SessionID)
	var session schema.AgentSession
	for {
		time.Sleep(5 * time.Second)
		if session.Status != schema.AgentSessionStatusCompleted {
			sessionSnap, err := sessionDoc.Get(ctx)
			if err != nil && status.Code(err) == codes.NotFound {
				continue
			} else if err != nil {
				return nil, errors.Wrap(err, "getting session document")
			}
			var newSession schema.AgentSession
			if err := sessionSnap.DataTo(&newSession); err != nil {
				return nil, errors.New("deserializing session data")
			}
			// As a proxy for iterations running, log any updates on the session.
			// TODO: Fetch iteration records as the come in and log those either instead of or in addition to this.
			if newSession.Updated != session.Updated {
				log.Printf("Session updated: %+v", newSession)
				session = newSession
			}
		}
		if session.Status == schema.AgentSessionStatusCompleted {
			log.Printf("Session %s completed", session.ID)
			break
		}
	}
	return &act.NoOutput{}, nil
}

// Command creates a new run-agent command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "run-agent --project <project> --api <URI> --ecosystem <ecosystem> --package <name> --version <version> --artifact <name> [--agent-iterations <max iterations>]",
		Short: "Run the agent on a single target",
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
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem")
	set.StringVar(&cfg.Package, "package", "", "the package name")
	set.StringVar(&cfg.Version, "version", "", "the version of the package")
	set.StringVar(&cfg.Artifact, "artifact", "", "the artifact name")
	set.IntVar(&cfg.AgentIterations, "agent-iterations", 3, "maximum number of agent iterations before giving up")
	return set
}
