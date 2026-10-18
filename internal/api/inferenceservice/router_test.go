// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
)

func TestRouterRoutesBySize(t *testing.T) {
	oversize := api.AsStatus(codes.FailedPrecondition, errors.New("failed to infer strategy: repository exceeds the size limit: https://github.com/org/big, archive is 5000 MiB, limit 4096 MiB"), api.AsDetail(rebuild.InferenceErrorDetail{Kind: rebuild.FailureOversize}))
	for _, tc := range []struct {
		name     string
		hint     schema.SizeHint
		noJumbo  bool // no jumbo service configured
		standard error
		jumboErr error
		want     []string
	}{
		{name: "Standard", want: []string{"standard"}},
		{name: "JumboHint", hint: schema.JumboSize, want: []string{"jumbo"}},
		{name: "Oversize", standard: oversize, want: []string{"standard", "jumbo"}},
		{name: "UnavailableForOtherReasons", standard: api.ErrUnavailable, want: []string{"standard"}},
		{name: "Failure", standard: api.AsStatus(codes.Internal, errors.New("no git ref")), want: []string{"standard"}},
		{name: "OversizeWithoutJumboService", noJumbo: true, standard: oversize, want: []string{"standard"}},
		{name: "OversizeJumboFailure", standard: oversize, jumboErr: api.AsStatus(codes.Internal, errors.New("no git ref")), want: []string{"standard", "jumbo"}},
		{name: "FailedPreconditionWithoutKind", standard: api.AsStatus(codes.FailedPrecondition, errors.New("failed to infer strategy: repository exceeds the size limit")), want: []string{"standard"}},
		{name: "JumboHintWithoutJumboService", noJumbo: true, hint: schema.JumboSize, want: []string{"standard"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			var last error // what the last service called returned
			r := Router{
				Standard: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
					calls = append(calls, "standard")
					if last = tc.standard; last != nil {
						return nil, last
					}
					return &schema.StrategyOneOf{}, nil
				},
			}
			if !tc.noJumbo {
				r.Jumbo = func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
					calls = append(calls, "jumbo")
					if last = tc.jumboErr; last != nil {
						return nil, last
					}
					return &schema.StrategyOneOf{}, nil
				}
			}
			s, err := r.Infer(context.Background(), schema.InferenceRequest{Package: "p", Version: "1"}, tc.hint)
			if err != last {
				t.Errorf("Infer() error = %v, want the last service's %v", err, last)
			}
			if (s == nil) != (last != nil) {
				t.Errorf("Infer() strategy = %v, last service's error = %v", s, last)
			}
			if !slices.Equal(calls, tc.want) {
				t.Errorf("calls = %v, want %v", calls, tc.want)
			}
		})
	}
}

func TestRouterWaitsForJumboRoom(t *testing.T) {
	orig := jumboRetryDelay
	jumboRetryDelay = time.Millisecond
	defer func() { jumboRetryDelay = orig }()
	var calls int
	r := Router{
		Jumbo: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
			calls++
			switch calls {
			case 1:
				return nil, api.ErrExhausted
			case 2:
				return nil, errors.New("fetching version: " + api.ErrExhausted.Error())
			case 3:
				return nil, errors.New("non-OK response: 500 Internal Server Error: The request was aborted because there was no available instance.")
			}
			return &schema.StrategyOneOf{}, nil
		},
	}
	if _, err := r.Infer(context.Background(), schema.InferenceRequest{Package: "p", Version: "1"}, schema.JumboSize); err != nil {
		t.Fatalf("infer() = %v", err)
	}
	if calls != 4 {
		t.Errorf("jumbo calls = %d, want 4 (three turned away for room)", calls)
	}
}

func TestRouterStopsWaitingWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Router{
		Jumbo: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
			return nil, api.ErrExhausted
		},
	}
	if _, err := r.Infer(ctx, schema.InferenceRequest{Package: "p", Version: "1"}, schema.JumboSize); !errors.Is(err, context.Canceled) {
		t.Fatalf("infer() = %v, want context.Canceled", err)
	}
}
