// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"

	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type agentLogic struct{}

func NewDefaultAgentLogic() AgentLogic {
	return &agentLogic{}
}

// TODO: Add tools for exploring the git repo
func (l *agentLogic) ProposeStrategy(ctx context.Context, t rebuild.Target, prev *schema.AgentIteration) (*schema.StrategyOneOf, error) {
	// For the first iteration, use our regular inference logic.
	// This allows the agent to benefit from the rest of our infrastructure improvements.
	if prev == nil {
		req := schema.InferenceRequest{
			Ecosystem: t.Ecosystem,
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
		}
		// TODO: Move inference storage to the deps, allowing us to re-use the same storage.
		deps := &inferenceservice.InferDeps{
			HTTPClient: http.DefaultClient,
			GitCache:   nil,
		}
		resp, err := inferenceservice.Infer(ctx, req, deps)
		if err != nil {
			return nil, errors.Wrap(err, "inferring initial strategy")
		}
		return resp, nil
	}
	// TODO: Implement actual agent logic here
	return nil, errors.New("agent logic not yet implemented")
}
