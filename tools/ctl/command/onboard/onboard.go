// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package onboard implements the commands that bring packages into oss-rebuild coverage.
package onboard

import (
	"context"
	"net/http"
	"net/url"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/spf13/cobra"
	"google.golang.org/genai"
)

// Deps carries the external clients shared by the onboard subcommands, so
// tests can substitute fakes. Clients that bind to flag values enter as
// constructors.
type Deps struct {
	IO        cli.IO
	FSFor     func(ctx context.Context, uri string) (billy.Filesystem, string, error)
	BigQuery  func(ctx context.Context, project string) (queryClient, error)
	Firestore func(ctx context.Context, project string) (*firestore.Client, error)
	GenAI     func(ctx context.Context, project, location string) (*genai.Client, error)
	APIClient func(ctx context.Context, endpoint string) (*http.Client, *url.URL, error)
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps wires the production dependencies.
func InitDeps(ctx context.Context) (*Deps, error) {
	return &Deps{
		FSFor:    billyx.NewResolver().FS,
		BigQuery: newBigQuery,
		Firestore: func(ctx context.Context, project string) (*firestore.Client, error) {
			return firestore.NewClient(ctx, project)
		},
		GenAI:     newGenAIClient,
		APIClient: apiClient,
	}, nil
}

// Command returns the parent `onboard` command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Bring packages into rebuild coverage",
	}
	cmd.AddCommand(priorityCommand(), enqueueCommand(), dispatchCommand(), statusCommand())
	return cmd
}
