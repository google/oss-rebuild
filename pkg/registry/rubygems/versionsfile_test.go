// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func partial(body, contentRange string) *http.Response {
	return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Range": {contentRange}}, Body: httpxtest.Body(body)}
}

func TestVersionsFile(t *testing.T) {
	client := &httpxtest.MockClient{
		Calls: []httpxtest.Call{
			{Response: partial("created_at: 2026-10-01T00:00:04Z\n---\n- 1 419d8a97", "bytes 0-127/5000")},
			{Response: partial("resque-cleaner 0.5.1 e04b\nnokogiri 1.19.4,1.19.4-x86_64-linux,-1.19.3 9201\nslim", "bytes 4900-4999/5000")},
			{Response: &http.Response{StatusCode: http.StatusOK, Body: httpxtest.Body("the whole file")}},
		},
		SkipURLValidation: true,
	}
	f := VersionsFile{Client: client}
	createdAt, size, err := f.Head(context.Background())
	if err != nil || createdAt != "2026-10-01T00:00:04Z" || size != 5000 {
		t.Fatalf("Head() = (%q, %d, %v)", createdAt, size, err)
	}
	got, next, err := f.Tail(context.Background(), 4900, size)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	want := []Appended{
		{Name: "resque-cleaner", Versions: []string{"0.5.1"}},
		{Name: "nokogiri", Versions: []string{"1.19.4", "1.19.4-x86_64-linux", "-1.19.3"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Tail mismatch (-want +got):\n%s", diff)
	}
	// The partial last line waits for the next read.
	if wantNext := int64(4900 + len("resque-cleaner 0.5.1 e04b\nnokogiri 1.19.4,1.19.4-x86_64-linux,-1.19.3 9201\n")); next != wantNext {
		t.Errorf("next = %d, want %d", next, wantNext)
	}
	// Nothing past the end is fetched.
	if got, n, err := f.Tail(context.Background(), size, size); err != nil || got != nil || n != size || client.CallCount() != 2 {
		t.Errorf("Tail at the end = (%v, %d, %v) after %d calls", got, n, err, client.CallCount())
	}
	// A full response to a range request is refused.
	if _, _, err := f.Tail(context.Background(), 10, 20); err == nil {
		t.Error("Tail accepted a non-partial response")
	}
}
