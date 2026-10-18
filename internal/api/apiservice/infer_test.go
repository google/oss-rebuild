// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
)

func TestInferEndpointRetriesOversizeOnJumbo(t *testing.T) {
	deps := &InferDeps{Router: inferenceservice.Router{
		Standard: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
			return nil, api.AsStatus(codes.FailedPrecondition, errors.New("too big"), api.AsDetail(rebuild.InferenceErrorDetail{Kind: rebuild.FailureOversize}))
		},
		Jumbo: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
			return &schema.StrategyOneOf{}, nil
		},
	}}
	if _, err := Infer(context.Background(), schema.InferenceRequest{Package: "p", Version: "1"}, deps); err != nil {
		t.Errorf("Infer() = %v", err)
	}
}

func TestGetStrategyRecordsInferringServiceVersion(t *testing.T) {
	oversize := api.AsStatus(codes.FailedPrecondition, errors.New("too big"), api.AsDetail(rebuild.InferenceErrorDetail{Kind: rebuild.FailureOversize}))
	for _, tc := range []struct {
		name     string
		hint     schema.SizeHint
		noJumbo  bool // no jumbo service configured
		standard error
		want     string
		wantAsks []string // services asked for their version
	}{
		{name: "Standard", want: "standard-rev", wantAsks: []string{"standard"}},
		{name: "JumboHint", hint: schema.JumboSize, want: "jumbo-rev", wantAsks: []string{"jumbo"}},
		{name: "Oversize", standard: oversize, want: "jumbo-rev", wantAsks: []string{"standard", "jumbo"}},
		{name: "JumboHintWithoutJumboService", noJumbo: true, hint: schema.JumboSize, want: "standard-rev", wantAsks: []string{"standard"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asks []string
			version := func(service, rev string) api.StubFn[schema.VersionRequest, schema.VersionResponse] {
				return func(context.Context, schema.VersionRequest) (*schema.VersionResponse, error) {
					asks = append(asks, service)
					return &schema.VersionResponse{Version: rev}, nil
				}
			}
			strategy := schema.NewStrategyOneOf(&rebuild.LocationHint{})
			deps := &RebuildPackageDeps{
				InferStub: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
					if tc.standard != nil {
						return nil, tc.standard
					}
					return &strategy, nil
				},
				InferVersionStub: version("standard", "standard-rev"),
			}
			if !tc.noJumbo {
				deps.JumboInferStub = func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
					return &strategy, nil
				}
				deps.JumboInferVersionStub = version("jumbo", "jumbo-rev")
			}
			res, err := getStrategy(context.Background(), deps, rebuild.Target{Package: "p", Version: "1"}, false, tc.hint)
			if err != nil {
				t.Fatalf("getStrategy() = %v", err)
			}
			if got := res.Inference.Version; got != tc.want {
				t.Errorf("recorded version = %q, want %q", got, tc.want)
			}
			if !slices.Equal(asks, tc.wantAsks) {
				t.Errorf("version asks = %v, want %v", asks, tc.wantAsks)
			}
		})
	}
}
