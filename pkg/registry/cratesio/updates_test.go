// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestUpdatesFeed(t *testing.T) {
	body := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:crates="https://crates.io/">
    <channel>
        <title>crates.io: recent updates</title>
        <item>
            <title>New crate version published: uv-torch v0.0.95</title>
            <link>https://crates.io/crates/uv-torch/0.0.95</link>
            <pubDate>Tue, 20 Oct 2026 01:16:04 +0000</pubDate>
            <crates:name>uv-torch</crates:name>
            <crates:version>0.0.95</crates:version>
        </item>
        <item>
            <title>New crate version published: Inflector v0.11.4</title>
            <pubDate>Tue, 20 Oct 2026 00:16:15 +0000</pubDate>
            <crates:name>Inflector</crates:name>
            <crates:version>0.11.4</crates:version>
        </item>
    </channel>
</rss>`
	client := &httpxtest.MockClient{
		Calls:        []httpxtest.Call{{URL: "https://static.crates.io/rss/updates.xml", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(body)}}},
		URLValidator: httpxtest.NewURLValidator(t),
	}
	got, err := UpdatesFeed{Client: client}.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Update{
		{Name: "uv-torch", Version: "0.0.95", Published: time.Date(2026, 10, 20, 1, 16, 4, 0, time.UTC)},
		{Name: "Inflector", Version: "0.11.4", Published: time.Date(2026, 10, 20, 0, 16, 15, 0, time.UTC)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read mismatch (-want +got):\n%s", diff)
	}
}
