// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"github.com/pkg/errors"

	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type GradleBuild struct {
	rebuild.Location
	rebuild.Target

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
}

var _ rebuild.Strategy = &GradleBuild{}

func (b *GradleBuild) ToWorkflow() (*rebuild.WorkflowStrategy, error) {
	jdkVersionURL, exists := JDKDownloadURLs[b.JDKVersion]
	if !exists {
		return nil, errors.Errorf("no download URL for JDK version %s", b.JDKVersion)
	}

	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "maven/setup-java",
			With: map[string]string{
				"versionURL": jdkVersionURL,
			},
		}},
		Build: []flow.Step{
			{
				Uses: "maven/export-java",
			},
			{
				Uses: "maven/gradle-build",
			},
			{
				Uses: "maven/move-gradle-build-output",
				With: map[string]string{
					"targetName": b.Artifact,
				},
			},
		},
		OutputDir: "",
	}, nil
}

func (b *GradleBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	workflow, err := b.ToWorkflow()
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return workflow.GenerateFor(t, be)
}
