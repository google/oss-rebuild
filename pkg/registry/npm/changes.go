// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var replicateURL = urlx.MustParse("https://replicate.npmjs.com")

// Change is one change to a package document in the replication feed, such as
// a publish, an unpublish, or a dist-tag move.
type Change struct {
	Seq     int64  `json:"seq"`
	ID      string `json:"id"` // the package name
	Deleted bool   `json:"deleted"`
}

// Replication reads the registry's replication feed, in which every change
// to a package document gets a sequence number greater than the last. A
// reader that keeps the last number it saw can resume from it without losing
// a change. The feed names the package, not the version.
type Replication struct {
	Client httpx.BasicClient
}

// LastSeq returns the sequence number of a recent change. The root document
// it reads is cached, so it can trail the feed by a few changes.
func (r Replication) LastSeq(ctx context.Context) (int64, error) {
	var root struct {
		UpdateSeq int64 `json:"update_seq"`
	}
	if err := r.get(ctx, replicateURL.String(), &root); err != nil {
		return 0, err
	}
	return root.UpdateSeq, nil
}

// Since returns up to limit changes after seq, oldest first, and the
// sequence number to resume from.
func (r Replication) Since(ctx context.Context, seq int64, limit int) ([]Change, int64, error) {
	u := replicateURL.JoinPath("registry", "_changes")
	q := u.Query()
	q.Set("since", strconv.FormatInt(seq, 10))
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()
	var page struct {
		Results []Change `json:"results"`
		LastSeq int64    `json:"last_seq"`
	}
	if err := r.get(ctx, u.String(), &page); err != nil {
		return nil, 0, err
	}
	return page.Results, max(page.LastSeq, seq), nil
}

func (r Replication) get(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.Wrap(errors.New(resp.Status), "reading the replication feed")
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
