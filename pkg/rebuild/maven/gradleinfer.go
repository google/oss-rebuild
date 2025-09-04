// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"log"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type GradleBuildInferer struct{}

var _ rebuild.StrategyInferer = &GradleBuildInferer{}

func (m *GradleBuildInferer) Infer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig, commitObject *object.Commit) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}

	var ref string
	if tagGuess != "" {
		ref = tagGuess
		log.Printf("using tag heuristic ref: %s", tagGuess[:9])
	} else {
		return nil, errors.Errorf("no valid git ref")
	}

	// Infer JDK for Gradle
	jdk, err := inferOrFallbackToDefaultJDK(ctx, name, version, mux)
	if err != nil {
		return nil, errors.Wrap(err, "fetching JDK")
	}

	return &GradleBuild{
		Location: rebuild.Location{
			Repo: repoConfig.URI,
			Dir:  "",
			Ref:  ref,
		},
		Target:     t,
		JDKVersion: jdk,
	}, nil
}
