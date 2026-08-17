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
	it := c.Collection(campaignsCollection).Documents(ctx)
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
