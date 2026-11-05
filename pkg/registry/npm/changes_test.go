// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestReplicationSince(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []Change
		next       int64
	}{
		{
			name: "Changes",
			body: `{"results":[{"seq":138142123,"id":"@stacksjs/iconify-entypo","changes":[{"rev":"329-bf"}]},{"seq":138142125,"id":"gone","changes":[{"rev":"3-0c"}],"deleted":true}],"last_seq":138142125}`,
			want: []Change{{Seq: 138142123, ID: "@stacksjs/iconify-entypo"}, {Seq: 138142125, ID: "gone", Deleted: true}},
			next: 138142125,
		},
		{
			name: "NoneYet",
			body: `{"results":[],"last_seq":0}`,
			want: []Change{},
			next: 138142120,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &httpxtest.MockClient{
				Calls:        []httpxtest.Call{{URL: "https://replicate.npmjs.com/registry/_changes?limit=2&since=138142120", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(tc.body)}}},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			got, next, err := Replication{Client: client}.Since(context.Background(), 138142120, 2)
			if err != nil {
				t.Fatalf("Since: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Since mismatch (-want +got):\n%s", diff)
			}
			if next != tc.next {
				t.Errorf("next = %d, want %d", next, tc.next)
			}
		})
	}
}
