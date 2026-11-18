// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"cloud.google.com/go/pubsub/v2"
	pb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	// WaiterSubscriptionPrefix prefixes ephemeral waiter subscription IDs.
	// NOTE: Matched by the IAM condition in deployments/iam.tf.
	WaiterSubscriptionPrefix = "gcb-waiter-"
	// terminalStatusFilter drops non-terminal Cloud Build lifecycle transitions server-side.
	terminalStatusFilter = `attributes.status != "STATUS_UNKNOWN" AND attributes.status != "PENDING" AND attributes.status != "QUEUED" AND attributes.status != "WORKING"`
	// waiterMessageRetention is Pub/Sub's 10m minimum message retention duration.
	waiterMessageRetention = 10 * time.Minute
	// waiterExpirationTTL is Pub/Sub's 24h minimum idle TTL so leaked subscriptions expire.
	waiterExpirationTTL = 24 * time.Hour
	// completedBuildTTL empirically bounds how long a completion stays recorded for a wait that starts after it arrives.
	completedBuildTTL = 10 * time.Minute
	// waiterDeleteTimeout empirically bounds subscription deletion during shutdown.
	waiterDeleteTimeout = 5 * time.Second
)

var topicPat = regexp.MustCompile(`^projects/([^/]+)/topics/([^/]+)$`)

// WaiterConfig configures a Waiter.
type WaiterConfig struct {
	Client *pubsub.Client
	Topic  string // projects/<project>/topics/<topic>
}

// Waiter watches a Cloud Build Pub/Sub notification topic on an ephemeral
// per-process subscription and wakes local WaitForOperation calls on terminal
// build updates. Ack state in Pub/Sub is isolated per subscription, so acking
// every message keeps this subscription empty without affecting other subscribers.
type Waiter struct {
	done      chan struct{}
	close     func() error
	mu        sync.Mutex
	waiters   map[string]chan struct{}
	completed map[string]time.Time
}

// NewWaiter creates an ephemeral filtered subscription on cfg.Topic and starts
// receiving completion events in the background.
func NewWaiter(ctx context.Context, cfg WaiterConfig) (*Waiter, error) {
	m := topicPat.FindStringSubmatch(cfg.Topic)
	if m == nil {
		return nil, errors.Errorf("invalid topic %q: want projects/<project>/topics/<topic>", cfg.Topic)
	}
	if cfg.Client == nil {
		return nil, errors.New("pubsub client is required")
	}
	subPB := waiterSubscription(m[1], cfg.Topic)
	if _, err := cfg.Client.SubscriptionAdminClient.CreateSubscription(ctx, subPB); err != nil {
		return nil, errors.Wrap(err, "creating waiter subscription")
	}
	sub := cfg.Client.Subscriber(subPB.Name)
	// Messages are acked on receipt, so disable lease extension. Zero would mean the 60m default.
	sub.ReceiveSettings.MaxExtension = -1
	return newWaiter(
		func(rctx context.Context, notify func(string)) error {
			return sub.Receive(rctx, func(_ context.Context, msg *pubsub.Message) {
				if id := msg.Attributes["buildId"]; id != "" {
					notify(id)
				}
				msg.Ack()
			})
		},
		func() error {
			delCtx, cancel := context.WithTimeout(context.Background(), waiterDeleteTimeout)
			defer cancel()
			err := cfg.Client.SubscriptionAdminClient.DeleteSubscription(delCtx, &pb.DeleteSubscriptionRequest{Subscription: subPB.Name})
			if status.Code(err) == codes.NotFound {
				return nil
			}
			return errors.Wrap(err, "deleting waiter subscription")
		},
	), nil
}

func waiterSubscription(project, topic string) *pb.Subscription {
	return &pb.Subscription{
		Name:                     fmt.Sprintf("projects/%s/subscriptions/%s%s", project, WaiterSubscriptionPrefix, uuid.NewString()),
		Topic:                    topic,
		AckDeadlineSeconds:       10,
		MessageRetentionDuration: durationpb.New(waiterMessageRetention),
		ExpirationPolicy:         &pb.ExpirationPolicy{Ttl: durationpb.New(waiterExpirationTTL)},
		Filter:                   terminalStatusFilter,
	}
}

func newWaiter(recv func(ctx context.Context, notify func(string)) error, cleanup func() error) *Waiter {
	recvCtx, cancel := context.WithCancel(context.Background())
	w := &Waiter{
		done:      make(chan struct{}),
		waiters:   make(map[string]chan struct{}),
		completed: make(map[string]time.Time),
	}
	var streamErr error
	go func() {
		if err := recv(recvCtx, w.notify); recvCtx.Err() == nil {
			streamErr = err
		}
		close(w.done)
	}()
	w.close = sync.OnceValue(func() error {
		cancel()
		<-w.done
		if err := cleanup(); err != nil {
			return err
		}
		return streamErr
	})
	return w
}

// Close stops the background receiver and deletes the ephemeral subscription.
func (w *Waiter) Close() error {
	if w == nil {
		return nil
	}
	return w.close()
}

func (w *Waiter) notify(buildID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-completedBuildTTL)
	for id, t := range w.completed {
		if t.Before(cutoff) {
			delete(w.completed, id)
		}
	}
	w.completed[buildID] = now
	if ch, ok := w.waiters[buildID]; ok {
		delete(w.waiters, buildID)
		close(ch)
	}
}

func (w *Waiter) alive() bool {
	if w == nil {
		return false
	}
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

// wait blocks until buildID completes on Pub/Sub, ctx is done, or the waiter stops.
// It returns true only when buildID was observed complete.
func (w *Waiter) wait(ctx context.Context, buildID string) bool {
	if !w.alive() || buildID == "" || ctx.Err() != nil {
		return false
	}
	w.mu.Lock()
	if t, ok := w.completed[buildID]; ok && time.Since(t) < completedBuildTTL {
		w.mu.Unlock()
		return true
	}
	ch, ok := w.waiters[buildID]
	if !ok {
		ch = make(chan struct{})
		w.waiters[buildID] = ch
	}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.waiters[buildID] == ch {
			delete(w.waiters, buildID)
		}
		w.mu.Unlock()
	}()
	select {
	case <-ch:
		return true
	case <-w.done:
		return false
	case <-ctx.Done():
		return false
	}
}
