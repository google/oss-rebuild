// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"encoding/xml"
	"net/http"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var updatesURL = urlx.MustParse("https://static.crates.io/rss/updates.xml")

// Update is one version publish.
type Update struct {
	Name      string
	Version   string
	Published time.Time
}

// UpdatesFeed reads crates.io's RSS feed of recent version publishes, which
// covers about the last hour. A reader that polls more often than that sees
// every publish. The feed has no position to resume from, so a reader whose
// last read is older than the feed's oldest item has missed publishes.
type UpdatesFeed struct {
	Client httpx.BasicClient
}

// Read returns the feed's publishes, newest first.
func (f UpdatesFeed) Read(ctx context.Context) ([]Update, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, updatesURL.String(), nil)
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Wrap(errors.New(resp.Status), "reading the updates feed")
	}
	var rss struct {
		Items []struct {
			Name    string `xml:"https://crates.io/ name"`
			Version string `xml:"https://crates.io/ version"`
			PubDate string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return nil, errors.Wrap(err, "decoding the updates feed")
	}
	out := make([]Update, 0, len(rss.Items))
	for _, it := range rss.Items {
		at, err := time.Parse(time.RFC1123Z, it.PubDate)
		if err != nil {
			return nil, errors.Wrapf(err, "parsing the publish time of %s %s", it.Name, it.Version)
		}
		out = append(out, Update{Name: it.Name, Version: it.Version, Published: at.UTC()})
	}
	return out, nil
}
