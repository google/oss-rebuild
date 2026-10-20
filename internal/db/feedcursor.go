// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"time"

	"cloud.google.com/go/firestore"
)

// FeedCursor is how far onboarding has read one registry's change feed. The
// cursor is a serial, a sequence number, a file position, or a publish time,
// whichever the feed uses. Updated is the last read that succeeded, so a stale
// one means the feed is not being followed.
type FeedCursor struct {
	Ecosystem string    `firestore:"ecosystem"`
	Cursor    string    `firestore:"cursor"`
	Updated   time.Time `firestore:"updated"`
}

// FeedCursors are keyed by ecosystem.
type FeedCursors = Resource[FeedCursor, string]

const feedCursorsCollection = "feed_cursors"

func feedCursorPath(c FeedCursor) []string { return feedCursorKey(c.Ecosystem) }

func feedCursorKey(ecosystem string) []string { return []string{feedCursorsCollection, ecosystem} }

func NewFirestoreFeedCursors(c *firestore.Client) FeedCursors {
	return &firestoreResource[FeedCursor, string]{client: c, pathFor: feedCursorPath, pathForKey: feedCursorKey}
}

func NewMemoryFeedCursors() FeedCursors {
	return &memoryResource[FeedCursor, string]{data: map[string]FeedCursor{}, pathFor: feedCursorPath, pathForKey: feedCursorKey}
}
