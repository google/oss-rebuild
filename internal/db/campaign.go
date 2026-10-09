// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"google.golang.org/api/iterator"
)

const campaignsCollection = "scheduler_campaigns"

// Campaigns persists the queue state of each onboarding campaign.
type Campaigns = Resource[scheduler.Campaign, rebuild.Target]

func campaignKey(t rebuild.Target) []string {
	return []string{campaignsCollection, scheduler.TargetID(t)}
}

func campaignPath(c scheduler.Campaign) []string { return campaignKey(c.Target()) }

// NewFirestoreCampaigns returns a Firestore-backed queue-state store.
func NewFirestoreCampaigns(c *firestore.Client) Campaigns {
	return &firestoreResource[scheduler.Campaign, rebuild.Target]{client: c, pathFor: campaignPath, pathForKey: campaignKey}
}

// NewMemoryCampaigns returns an in-memory queue-state store for tests.
func NewMemoryCampaigns() Campaigns {
	return &memoryResource[scheduler.Campaign, rebuild.Target]{data: map[string]scheduler.Campaign{}, pathFor: campaignPath, pathForKey: campaignKey}
}

// ListCampaigns returns every queue-state document. The onboarded set is
// small enough that a full scan is acceptable. Readers sort and filter in
// memory, which keeps the collection free of composite indexes.
func ListCampaigns(ctx context.Context, c *firestore.Client) ([]scheduler.Campaign, error) {
	return collectCampaigns(c.Collection(campaignsCollection).Documents(ctx))
}

// ListActiveCampaigns returns the queued and in-flight campaigns, the only
// states dispatch acts on. Terminal campaigns accumulate for as long as
// onboarding runs, so filtering server-side keeps the read proportional to
// queue depth. A single-field filter needs no composite index.
func ListActiveCampaigns(ctx context.Context, c *firestore.Client) ([]scheduler.Campaign, error) {
	q := c.Collection(campaignsCollection).
		Where("state", "in", []string{string(scheduler.StateQueued), string(scheduler.StateInFlight)})
	return collectCampaigns(q.Documents(ctx))
}

func collectCampaigns(it *firestore.DocumentIterator) ([]scheduler.Campaign, error) {
	defer it.Stop()
	var out []scheduler.Campaign
	for {
		snap, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var v scheduler.Campaign
		if err := snap.DataTo(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}
