// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package listener

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var (
	versionURLRegex = regexp.MustCompile(`https://pypi\.org/project/(?P<package>[^/]+)/(?P<version>[^/]+)/`)
)

type RSS struct {
	Channel Channel `xml:"channel"`
}

type Channel struct {
	Items []Item `xml:"item"`
}

type Item struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

type Event struct {
	Pkg     string
	Version string
	Time    time.Time
}

type listener struct {
	httpClient httpx.BasicClient
	rssURL     string
	latestEtag string
	handled    map[string]map[string]bool
	tracker    feed.Tracker
	apiURL     *url.URL
	mode       schema.ExecutionMode
	queue      taskqueue.Queue
}

func NewListener(
	httpClient httpx.BasicClient,
	rssURL string,
	tracker feed.Tracker,
	apiURL *url.URL,
	mode schema.ExecutionMode,
	queue taskqueue.Queue,
) *listener {
	return &listener{
		httpClient: httpClient,
		rssURL:     rssURL,
		latestEtag: "",
		handled:    make(map[string]map[string]bool),
		tracker:    tracker,
		apiURL:     apiURL,
		mode:       mode,
		queue:      queue,
	}
}

func (l *listener) trim() error {
	// TODO: trim the handled map.
	// Until this is implemented, any running instance of this consumer will grow the handled map linearly with time.
	return nil
}

func (l *listener) handle(ctx context.Context, events []Event) error {
	var count int
	var toRun []rebuild.Target
	for _, e := range events {
		t := rebuild.Target{
			Ecosystem: rebuild.PyPI,
			Package:   e.Pkg,
			Version:   e.Version,
			// TODO: Inlude the artifact from the event.
			Artifact: "",
		}
		tracked, err := l.tracker.IsTracked(schema.TargetEvent{}.From(t))
		if err != nil {
			return errors.Wrap(err, "checking if tracked")
		}
		if !tracked {
			continue
		}
		if _, ok := l.handled[e.Pkg]; !ok {
			l.handled[e.Pkg] = make(map[string]bool)
		}
		if _, ok := l.handled[e.Pkg][e.Version]; ok {
			continue
		}
		if slices.Contains(toRun, t) {
			continue
		}
		toRun = append(toRun, t)
		log.Printf("tracked package was updated: %s@%s at %v", e.Pkg, e.Version, e.Time)
		count += 1
	}
	if len(toRun) == 0 {
		return nil
	}
	switch l.mode {
	case schema.AttestMode:
		for _, msg := range feed.GroupForAttest(toRun, "pypi-rss") {
			if _, err := l.queue.Add(ctx, l.apiURL.JoinPath("rebuild").String(), msg); err != nil {
				return errors.Wrapf(err, "adding msg to queue %+v", msg)
			}
		}
	case schema.SmoketestMode:
		for _, msg := range feed.GroupForSmoketest(toRun, "pypi-rss") {
			if _, err := l.queue.Add(ctx, l.apiURL.JoinPath("smoketest").String(), msg); err != nil {
				return errors.Wrapf(err, "adding msg to queue %+v", msg)
			}
		}
	default:
		return errors.Errorf("invalid mode: %s", string(l.mode))
	}
	for _, t := range toRun {
		l.handled[t.Package][t.Version] = true
	}
	return l.trim()
}

func (l *listener) fetch(client *http.Client) ([]Event, error) {
	// TODO: Handle pagination.
	req, err := http.NewRequest(http.MethodGet, l.rssURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "making HTTP request")
	}
	req.Header.Set("If-None-Match", l.latestEtag)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "error making HTTP request")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return []Event{}, nil
	case http.StatusOK:
		l.latestEtag = resp.Header.Get("Etag")
	default:
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "error reading response body")
	}
	var data RSS
	err = xml.Unmarshal(body, &data)
	if err != nil {
		return nil, errors.Wrap(err, "parsing XML")
	}
	var events []Event
	for _, item := range data.Channel.Items {
		eventTime, err := time.Parse(time.RFC1123, item.PubDate)
		if err != nil {
			return nil, errors.Wrap(err, "parsing date")
		}
		matches := versionURLRegex.FindStringSubmatch(item.Link)
		if len(matches) != 3 {
			return nil, fmt.Errorf("unexpected link: '%s'", item.Link)
		}
		events = append(events, Event{
			Pkg:     matches[versionURLRegex.SubexpIndex("package")],
			Version: matches[versionURLRegex.SubexpIndex("version")],
			Time:    eventTime,
		})
	}
	slices.SortFunc(events, func(a, b Event) int {
		return a.Time.Compare(b.Time)
	})
	return events, nil
}

func (l *listener) Poll(ctx context.Context) error {
	events, err := l.fetch(http.DefaultClient)
	if err != nil {
		return err
	}
	log.Printf("Update %s spanning from %v to %v", l.latestEtag, events[0].Time, events[len(events)-1].Time)
	return l.handle(ctx, events)
}
