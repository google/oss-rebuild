// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package viewsession

import (
	"context"
	"flag"

	"cloud.google.com/go/firestore"
	gcs "cloud.google.com/go/storage"
	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	agentide "github.com/google/oss-rebuild/tools/ctl/ide/agent"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
)

// Config holds all configuration for the view-session command.
type Config struct {
	Project        string
	MetadataBucket string
	LogsBucket     string
	SessionID      string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.MetadataBucket == "" {
		return errors.New("metadata-bucket is required")
	}
	if c.LogsBucket == "" {
		return errors.New("logs-bucket is required")
	}
	if c.SessionID == "" {
		return errors.New("session-id is required")
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

func parseArgs(cfg *Config, args []string) error {
	if len(args) != 1 {
		return errors.New("expected exactly 1 argument: session-id")
	}
	cfg.SessionID = args[0]
	return nil
}

// Handler contains the business logic for viewing a session.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	// Fetch session
	sessionDoc := fire.Collection("agent_sessions").Doc(cfg.SessionID)
	sessionSnap, err := sessionDoc.Get(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "getting session document")
	}
	session := &schema.AgentSession{}
	if err := sessionSnap.DataTo(session); err != nil {
		return nil, errors.Wrap(err, "deserializing session data")
	}
	// Fetch iterations
	var iters []*schema.AgentIteration
	iter := sessionDoc.Collection("agent_iterations").Documents(ctx)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, errors.Wrap(err, "iterating over iterations")
		}
		iteration := &schema.AgentIteration{}
		if err := doc.DataTo(iteration); err != nil {
			return nil, errors.Wrap(err, "deserializing iteration data")
		}
		iters = append(iters, iteration)
	}
	gcsClient, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating gcs client")
	}
	app := tview.NewApplication()
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 'q' || event.Key() == tcell.KeyCtrlC {
			app.Stop()
			return nil
		}
		return event
	})
	v := agentide.NewSessionView(session, iters, agentide.SessionViewDeps{
		GCS:            gcsClient,
		App:            app,
		MetadataBucket: cfg.MetadataBucket,
		LogsBucket:     cfg.LogsBucket,
	})
	if err := v.Run(); err != nil {
		return nil, err
	}
	return &act.NoOutput{}, nil
}

// Command creates a new view-session command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "view-session --project <project> --metadata-bucket <bucket> --logs-bucket <bucket> <session-id>",
		Short: "View details of an agent session",
		Args:  cobra.ExactArgs(1),
		RunE: cli.RunE(
			&cfg,
			parseArgs,
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
	set.StringVar(&cfg.MetadataBucket, "metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	set.StringVar(&cfg.LogsBucket, "logs-bucket", "", "the gcs bucket where gcb logs are stored")
	return set
}
