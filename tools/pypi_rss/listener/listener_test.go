// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package listener

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type FakeTracker struct {
	Tracked map[rebuild.Ecosystem]map[string]bool
}

func (f *FakeTracker) IsTracked(e schema.ReleaseEvent) (bool, error) {
	if _, ok := f.Tracked[e.Ecosystem]; !ok {
		return false, nil
	}
	if _, ok := f.Tracked[e.Ecosystem][e.Package]; !ok {
		return false, nil
	}
	return true, nil
}

type FakeQueue struct {
	Err      error
	Messages []api.Message
}

var _ taskqueue.Queue = &FakeQueue{}

func (f *FakeQueue) Add(ctx context.Context, url string, msg api.Message) (*cloudtaskspb.Task, error) {
	f.Messages = append(f.Messages, msg)
	if f.Err != nil {
		return nil, f.Err
	}
	return nil, nil
}

func TestListenerHandle(t *testing.T) {
	ctx := context.Background()
	apiURL := urlx.MustParse("http://fake-api-url")
	testTime, _ := time.Parse(time.RFC1123, "Mon, 02 Jan 2006 15:04:05 MST")
	tests := []struct {
		name        string
		events      []Event
		tracked     map[rebuild.Ecosystem]map[string]bool
		handled     map[string]map[string]bool
		mode        schema.ExecutionMode
		wantHandled map[string]map[string]bool
		wantQueued  []api.Message
		wantErr     bool
	}{
		{
			name:        "No events",
			events:      []Event{},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}},
			mode:        schema.AttestMode,
			wantHandled: map[string]map[string]bool{},
			wantQueued:  nil,
			wantErr:     false,
		},
		{
			name:        "Untracked package",
			events:      []Event{{Pkg: "pkg1", Version: "1.0", Time: testTime}},
			tracked:     map[rebuild.Ecosystem]map[string]bool{}, // Empty tracked
			mode:        schema.AttestMode,
			wantHandled: map[string]map[string]bool{},
			wantQueued:  nil,
			wantErr:     false,
		},
		{
			name:        "Already handled",
			events:      []Event{{Pkg: "pkg1", Version: "1.0", Time: testTime}},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}},
			handled:     map[string]map[string]bool{"pkg1": {"1.0": true}},
			mode:        schema.AttestMode,
			wantHandled: map[string]map[string]bool{"pkg1": {"1.0": true}},
			wantQueued:  nil,
			wantErr:     false,
		},
		{
			name:        "Single event (Attest)",
			events:      []Event{{Pkg: "pkg1", Version: "1.0", Time: testTime}},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}},
			mode:        schema.AttestMode,
			wantHandled: map[string]map[string]bool{"pkg1": {"1.0": true}},
			wantQueued: []api.Message{
				schema.RebuildPackageRequest{
					Ecosystem: rebuild.PyPI,
					Package:   "pkg1",
					Version:   "1.0",
					ID:        "pypi-rss",
				},
			},
			wantErr: false,
		},
		{
			name:        "Single event (Smoketest)",
			events:      []Event{{Pkg: "pkg1", Version: "1.0", Time: testTime}},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}},
			mode:        schema.SmoketestMode,
			wantHandled: map[string]map[string]bool{"pkg1": {"1.0": true}},
			wantQueued: []api.Message{
				schema.SmoketestRequest{
					Ecosystem: rebuild.PyPI,
					Package:   "pkg1",
					Versions:  []string{"1.0"},
					ID:        "pypi-rss",
				},
			},
			wantErr: false,
		},
		{
			name: "Multiple events, same package",
			events: []Event{
				{Pkg: "pkg1", Version: "1.0", Time: testTime},
				{Pkg: "pkg1", Version: "1.1", Time: testTime},
			},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}},
			mode:        schema.SmoketestMode,
			wantHandled: map[string]map[string]bool{"pkg1": {"1.0": true, "1.1": true}},
			wantQueued: []api.Message{
				schema.SmoketestRequest{
					Ecosystem: rebuild.PyPI,
					Package:   "pkg1",
					Versions:  []string{"1.0", "1.1"},
					ID:        "pypi-rss",
				},
			},
			wantErr: false,
		},
		{
			name: "Multiple events, different packages",
			events: []Event{
				{Pkg: "pkg1", Version: "1.0", Time: testTime},
				{Pkg: "pkg2", Version: "2.0", Time: testTime},
			},
			tracked:     map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true, "pkg2": true}},
			mode:        schema.SmoketestMode,
			wantHandled: map[string]map[string]bool{"pkg1": {"1.0": true}, "pkg2": {"2.0": true}},
			wantQueued: []api.Message{
				schema.SmoketestRequest{
					Ecosystem: rebuild.PyPI,
					Package:   "pkg1",
					Versions:  []string{"1.0"},
					ID:        "pypi-rss",
				},
				schema.SmoketestRequest{
					Ecosystem: rebuild.PyPI,
					Package:   "pkg2",
					Versions:  []string{"2.0"},
					ID:        "pypi-rss",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.handled == nil {
				tt.handled = map[string]map[string]bool{}
			}
			l := &listener{
				rssURL:  "test-url", // Not actually used in this test
				handled: tt.handled,
				tracker: feed.TrackerFromFunc(func(e schema.ReleaseEvent) (bool, error) {
					if _, ok := tt.tracked[e.Ecosystem]; !ok {
						return false, nil
					}
					tracked, ok := tt.tracked[e.Ecosystem][e.Package]
					return ok && tracked, nil
				}),
				apiURL: apiURL,
				mode:   tt.mode,
				queue:  &FakeQueue{},
			}

			err := l.handle(ctx, tt.events)
			if (err != nil) != tt.wantErr {
				t.Errorf("handle() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if diff := cmp.Diff(l.handled, tt.wantHandled); diff != "" {
				t.Errorf("handle() handled map diff (-got +want):\n%s", diff)
			}

			fq := l.queue.(*FakeQueue)
			if diff := cmp.Diff(fq.Messages, tt.wantQueued); diff != "" {
				t.Errorf("handle() queued message diff (-got +want):\n%s", diff)
			}
		})
	}
}

func TestListenerHandleReturnsError(t *testing.T) {
	wantErr := errors.New("expected error")
	l := &listener{
		rssURL:  "test-url", // Not actually used in this test
		handled: map[string]map[string]bool{},
		tracker: &FakeTracker{Tracked: map[rebuild.Ecosystem]map[string]bool{rebuild.PyPI: {"pkg1": true}}},
		apiURL:  urlx.MustParse("http://api-url"),
		mode:    schema.AttestMode,
		queue:   &FakeQueue{Err: wantErr},
	}
	ctx := context.Background()
	testTime, _ := time.Parse(time.RFC1123, "Mon, 02 Jan 2006 15:04:05 MST")
	err := l.handle(ctx, []Event{{Pkg: "pkg1", Version: "1.0", Time: testTime}})
	if !errors.Is(err, wantErr) {
		t.Errorf("handle() error = %v, wantErr %v", err, wantErr)
		return
	}
}

func TestListenerFetch(t *testing.T) {
	testTime, _ := time.Parse(time.RFC1123, "Mon, 02 Jan 2006 15:04:05 MST")
	tests := []struct {
		name         string
		url          string
		haveEtag     string
		responseCode int
		responseBody string
		responseEtag string
		wantEvents   []Event
		wantErr      bool
	}{
		{
			name:         "Not Modified",
			url:          "/rss/updates.xml",
			responseCode: http.StatusNotModified,
			responseEtag: "etag-123",
			haveEtag:     "etag-123",
			wantEvents:   []Event{},
			wantErr:      false,
		},
		{
			name:         "OK - Single Event",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `
<?xml version="1.0"?>
<rss version="2.0">
	<channel>
		<item>
			<title>absl-py 1.2.3</title>
			<link>https://pypi.org/project/absl-py/1.2.3/</link>
			<pubDate>Mon, 02 Jan 2006 15:04:05 MST</pubDate>
		</item>
	</channel>
</rss>`,
			wantEvents: []Event{
				{Pkg: "absl-py", Version: "1.2.3", Time: testTime},
			},
			wantErr: false,
		},
		{
			name:         "OK - Multiple Events",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `
<?xml version="1.0"?>
<rss>
	<channel>
		<item>
			<title>absl-py 1.2.3</title>
			<link>https://pypi.org/project/absl-py/1.2.3/</link>
			<pubDate>Mon, 02 Jan 2006 15:04:05 MST</pubDate>
		</item>
		<item>
			<title>requests 2.28.1</title>
			<link>https://pypi.org/project/requests/2.28.1/</link>
			<pubDate>Mon, 02 Jan 2006 16:04:05 MST</pubDate>
		</item>
	</channel>
</rss>`,
			wantEvents: []Event{
				{Pkg: "absl-py", Version: "1.2.3", Time: testTime},
				{Pkg: "requests", Version: "2.28.1", Time: testTime.Add(time.Hour)}, // Add one hour
			},
			wantErr: false,
		},
		{
			name:         "OK - No Events",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `<?xml version="1.0"?><rss><channel></channel></rss>`,
			wantEvents:   nil,
			wantErr:      false,
		},
		{
			name:         "Error - Invalid XML",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `invalid xml`,
			wantEvents:   nil,
			wantErr:      true,
		},
		{
			name:         "Error - Invalid Link",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `
<?xml version="1.0"?>
<rss>
	<channel>
		<item>
			<title>absl-py 1.2.3</title>
			<link>invalid-link</link>
			<pubDate>Mon, 02 Jan 2006 15:04:05 MST</pubDate>
		</item>
	</channel>
</rss>`,
			wantEvents: nil,
			wantErr:    true,
		},
		{
			name:         "Error - Invalid Date",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusOK,
			responseEtag: "etag-345",
			responseBody: `
<?xml version="1.0"?>
<rss>
	<channel>
		<item>
			<title>absl-py 1.2.3</title>
			<link>https://pypi.org/project/absl-py/1.2.3/</link>
			<pubDate>invalid-date</pubDate>
		</item>
	</channel>
</rss>`,
			wantEvents: nil,
			wantErr:    true,
		},
		{
			name:         "Error - Unexpected Status Code",
			url:          "/rss/updates.xml",
			haveEtag:     "etag-123",
			responseCode: http.StatusInternalServerError,
			wantEvents:   nil,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != tt.haveEtag {
					t.Errorf("Expected If-None-Match header to be %q, got %q", tt.haveEtag, r.Header.Get("If-None-Match"))
				}
				w.Header().Set("Etag", tt.responseEtag)
				w.WriteHeader(tt.responseCode)
				if tt.responseCode == http.StatusOK { // Only write body for OK
					io.WriteString(w, tt.responseBody)
				}
			}))
			defer ts.Close()

			l := &listener{
				httpClient: http.DefaultClient,
				rssURL:     ts.URL + tt.url,              // Use the test server URL
				handled:    map[string]map[string]bool{}, // Not used in fetch
				tracker:    nil,                          // Not used in fetch
				apiURL:     &url.URL{},                   // Not used in fetch
				mode:       "",                           // Not used in fetch
				queue:      nil,                          // Not used in fetch
			}
			l.latestEtag = tt.haveEtag

			events, err := l.fetch(http.DefaultClient)

			if (err != nil) != tt.wantErr {
				t.Errorf("fetch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr { // Only check events if no error is expected
				if diff := cmp.Diff(tt.wantEvents, events); diff != "" {
					t.Errorf("fetch() events mismatch (-want +got):\n%s", diff)
				}
				if l.latestEtag != tt.responseEtag && tt.responseCode == http.StatusOK {
					t.Errorf("Expected Etag to be updated")
				}
			}
		})
	}
}
