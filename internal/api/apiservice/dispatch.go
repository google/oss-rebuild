// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"log"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/dispatch"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

// DispatchDeps binds a dispatch pass to this service's own rebuild and agent
// handlers, so a scheduled pass needs no route to the API from outside.
type DispatchDeps struct {
	Firestore *firestore.Client
	Rebuild   *CreateRebuildOpDeps
	Agent     *AgentCreateDeps
}

// Dispatch drives onboarding dispatch passes in-process. Overlapping passes
// are safe since campaign transitions are compare-and-swaps.
func Dispatch(ctx context.Context, req dispatch.Request, deps *DispatchDeps) (*dispatch.Report, error) {
	cfg, err := req.Config()
	if err != nil {
		return nil, err
	}
	d := &dispatch.Dispatcher{
		Err:       log.Writer(),
		Campaigns: db.NewFirestoreCampaigns(deps.Firestore),
		Budgets:   db.NewFirestoreBudgets(deps.Firestore),
		Attempts:  db.NewFirestoreAttempts(deps.Firestore),
		Sessions:  db.NewFirestoreSessions(deps.Firestore),
		Create:    api.Local(CreateRebuildOp, deps.Rebuild),
		Agent:     api.Local(AgentCreate, deps.Agent),
		Config:    cfg,
	}
	report, err := d.Run(ctx, func(ctx context.Context) ([]scheduler.Campaign, error) {
		return db.ListActiveCampaigns(ctx, deps.Firestore)
	}, func(ctx context.Context) ([]scheduler.Campaign, error) {
		return db.ListInFlightCampaigns(ctx, deps.Firestore)
	})
	if err != nil {
		return nil, err
	}
	log.Println(report)
	return report, nil
}
