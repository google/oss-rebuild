// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/oss-rebuild/internal/api/scratchhostservice"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Host labels. purpose and class select admission candidates. A retired
// host leaves the candidate set but stays visible to the reaper.
const (
	hostLabelPurposeKey = "purpose"
	hostLabelPurposeVal = "scratch-host"
	hostLabelClassKey   = "scratch_class"
	hostLabelRetiredKey = "scratch_retired"
)

func hostLabels(class schema.MachineClass) map[string]string {
	return map[string]string{
		hostLabelPurposeKey: hostLabelPurposeVal,
		hostLabelClassKey:   string(class),
	}
}

// hostMaxAge is how long a host takes new sessions. It sets how fast a
// loaded fleet rotates onto a new host image.
const hostMaxAge = 24 * time.Hour

// HostDialer returns the HTTP client and base URL for a host's broker,
// under the host's audience. Every request to a host goes through it.
type HostDialer func(host Instance) (httpx.BasicClient, *url.URL, error)

// HostClient calls host brokers' lifecycle routes. Exec routes use act stubs
// over the same HostDialer.
type HostClient struct{ Dial HostDialer }

// Admit boots a guest for scratchID on host. Idempotent on scratchID.
// Every refusal means try elsewhere.
func (h *HostClient) Admit(ctx context.Context, host Instance, scratchID string) error {
	client, base, err := h.Dial(host)
	if err != nil {
		return err
	}
	admit := api.Stub[scratchhostservice.AdmitRequest, scratchhostservice.Guest](client, base.JoinPath(scratchhostservice.RouteAdmit))
	if _, err := admit(ctx, scratchhostservice.AdmitRequest{ScratchID: scratchID}); err != nil {
		return errors.Wrapf(err, "admit on %s", host.Name)
	}
	return nil
}

// Delete destroys the guest serving scratchID and its exec records. A
// guest already gone is not an error.
func (h *HostClient) Delete(ctx context.Context, host Instance, scratchID string) error {
	client, base, err := h.Dial(host)
	if err != nil {
		return err
	}
	del := api.Stub[scratchhostservice.GuestRequest, act.NoOutput](client, base.JoinPath(scratchhostservice.RouteDelete))
	_, err = del(ctx, scratchhostservice.GuestRequest{ScratchID: scratchID})
	// NotFound means the guest is already gone.
	if err != nil && status.Code(err) != codes.NotFound {
		return errors.Wrapf(err, "delete guest %s on %s", scratchID, host.Name)
	}
	return nil
}

// Guests reports the host's live guests and capacity.
func (h *HostClient) Guests(ctx context.Context, host Instance) (scratchhostservice.GuestList, error) {
	client, base, err := h.Dial(host)
	if err != nil {
		return scratchhostservice.GuestList{}, err
	}
	list := api.Stub[scratchhostservice.ListRequest, scratchhostservice.GuestList](client, base.JoinPath(scratchhostservice.RouteList))
	out, err := list(ctx, scratchhostservice.ListRequest{})
	if err != nil {
		return scratchhostservice.GuestList{}, errors.Wrapf(err, "guests on %s", host.Name)
	}
	return *out, nil
}

// Healthz reports whether the host broker is answering, with a plain GET
// on its one open route.
func (h *HostClient) Healthz(ctx context.Context, host Instance) error {
	client, base, err := h.Dial(host)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.JoinPath(scratchhostservice.RouteHealthz).String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrapf(err, "healthz on %s", host.Name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("healthz on %s: status %d", host.Name, resp.StatusCode)
	}
	return nil
}

// GuestHealthz fails until the session's guest answers on its exec channel.
func (h *HostClient) GuestHealthz(ctx context.Context, host Instance, scratchID string) error {
	client, base, err := h.Dial(host)
	if err != nil {
		return err
	}
	probe := api.Stub[scratchhostservice.GuestRequest, act.NoOutput](client, base.JoinPath(scratchhostservice.RouteGuestHealth))
	if _, err := probe(ctx, scratchhostservice.GuestRequest{ScratchID: scratchID}); err != nil {
		return errors.Wrapf(err, "guest %s healthz on %s", scratchID, host.Name)
	}
	return nil
}

// listHosts returns the running instances matching labels across zones. A
// failing zone is logged and skipped: a short list costs at most a cold host
// create.
func listHosts(ctx context.Context, gce GCE, zones []string, labels map[string]string) []Instance {
	var out []Instance
	for _, zone := range zones {
		insts, err := gce.ListRunningInstances(ctx, zone, labels)
		if err != nil {
			log.Printf("listing hosts in %s: %v", zone, err)
			continue
		}
		out = append(out, insts...)
	}
	return out
}

// admissible reports whether a host may take new sessions.
func admissible(host Instance, now time.Time, maxAge time.Duration) bool {
	if _, retired := host.Labels[hostLabelRetiredKey]; retired {
		return false
	}
	return !olderThan(host.Created, now, maxAge)
}

// olderThan reports whether t is more than d before now. A zero t is
// infinitely old.
func olderThan(t, now time.Time, d time.Duration) bool {
	return t.IsZero() || now.Sub(t) > d
}

// stampAt returns the time a host's key label holds, false if it is unset.
func stampAt(host Instance, key string) (time.Time, bool) {
	stamp, ok := host.Labels[key]
	if !ok {
		return time.Time{}, false
	}
	unix, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		// An unreadable stamp reads as long past, which can only hasten
		// collection.
		return time.Time{}, true
	}
	return time.Unix(unix, 0).UTC(), true
}

// retire stamps a host out of the candidate set.
func retire(ctx context.Context, gce GCE, host Instance, now time.Time) error {
	return stamp(ctx, gce, host, now, hostLabelRetiredKey)
}

// stamp sets each of keys that host lacks to now, in one label write. The
// fingerprint makes a concurrent label write win, and the next pass retries.
func stamp(ctx context.Context, gce GCE, host Instance, now time.Time, keys ...string) error {
	labels := maps.Clone(host.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	n := len(labels)
	for _, key := range keys {
		if _, ok := labels[key]; !ok {
			labels[key] = fmt.Sprint(now.UTC().Unix())
		}
	}
	if len(labels) == n {
		return nil
	}
	return gce.SetLabels(ctx, host.Zone, host.Name, labels, host.LabelFingerprint)
}

// runConcurrently applies fn to each item, at most limit at a time. fn
// handles its own errors, so one item's failure never stops another.
func runConcurrently[T any](ctx context.Context, items []T, limit int, fn func(context.Context, T)) {
	var eg errgroup.Group
	eg.SetLimit(max(limit, 1))
	for _, item := range items {
		eg.Go(func() error { fn(ctx, item); return nil })
	}
	_ = eg.Wait()
}
