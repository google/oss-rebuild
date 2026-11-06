// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
)

// Router routes inferences between a standard service and a jumbo service
// that holds one large repository per instance.
type Router struct {
	Standard api.StubFn[schema.InferenceRequest, schema.StrategyOneOf]
	Jumbo    api.StubFn[schema.InferenceRequest, schema.StrategyOneOf] // nil sends everything to Standard
}

// Infer runs on Jumbo when hint is JumboSize or Standard refuses the repository
// for its size, and on Standard otherwise.
func (r Router) Infer(ctx context.Context, req schema.InferenceRequest, hint schema.SizeHint) (*schema.StrategyOneOf, error) {
	if r.Jumbo == nil {
		return r.Standard(ctx, req)
	}
	if hint == schema.JumboSize {
		return r.inferJumbo(ctx, req)
	}
	s, err := r.Standard(ctx, req)
	if err != nil && RefusedForSize(err) {
		log.Printf("inferring %s@%s on the jumbo service: %v", req.Package, req.Version, err)
		return r.inferJumbo(ctx, req)
	}
	return s, err
}

// jumboRetryDelay paces asks of a full jumbo service. Room takes a new
// instance's cold start or a running inference's finish, so asking sooner only
// adds rejections.
var jumboRetryDelay = 20 * time.Second

// inferJumbo waits out capacity rejections, which a burst of large
// repositories causes until instances finish or more start.
func (r Router) inferJumbo(ctx context.Context, req schema.InferenceRequest) (*schema.StrategyOneOf, error) {
	for {
		s, err := r.Jumbo(ctx, req)
		if err == nil || !scheduler.IsCapacityRejection(err.Error()) {
			return s, err
		}
		select {
		case <-ctx.Done():
			return nil, errors.Wrapf(context.Cause(ctx), "waiting for room on the jumbo service: %v", err)
		case <-time.After(jumboRetryDelay):
		}
	}
}

// RefusedForSize reports a refusal for a repository over the size limit.
func RefusedForSize(err error) bool {
	d, ok := api.DetailOf[rebuild.InferenceErrorDetail](err)
	return ok && d.Kind == rebuild.FailureOversize
}
