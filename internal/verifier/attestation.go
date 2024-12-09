// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package verifier

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/pkg/errors"
)

const (
	// RebuildBuildType is the SLSA build type used for rebuild attestations.
	RebuildBuildType = "https://docs.oss-rebuild.dev/builds/Rebuild@v0.1"
	// ArtifactEquivalenceBuildType is the SLSA build type used for artifact equivalence attestations.
	ArtifactEquivalenceBuildType = "https://docs.oss-rebuild.dev/builds/ArtifactEquivalence@v0.1"
)

// CreateAttestations creates the SLSA attestations associated with a rebuild.
func CreateAttestations(ctx context.Context, input rebuild.Input, finalStrategy rebuild.Strategy, id string, rb, up ArtifactSummary, metadata rebuild.AssetStore, buildDef rebuild.Location) (equivalence, build *in_toto.ProvenanceStatementSLSA1, err error) {
	t, manualStrategy := input.Target, input.Strategy
	var dockerfile []byte
	{
		r, err := metadata.Reader(ctx, rebuild.DockerfileAsset.For(t))
		if err != nil {
			return nil, nil, errors.Wrap(err, "opening rebuild Dockerfile")
		}
		defer checkClose(r)
		dockerfile, err = io.ReadAll(r)
		if err != nil {
			return nil, nil, errors.Wrap(err, "reading rebuild Dockerfile")
		}
	}
	var buildInfo rebuild.BuildInfo
	{
		r, err := metadata.Reader(ctx, rebuild.BuildInfoAsset.For(t))
		if err != nil {
			return nil, nil, errors.Wrap(err, "opening rebuild build info file")
		}
		defer checkClose(r)
		if err := json.NewDecoder(r).Decode(&buildInfo); err != nil {
			return nil, nil, errors.Wrap(err, "parsing rebuild build info file")
		}
	}
	builder := slsa1.Builder{
		// TODO: Make the host configurable.
		ID: "https://docs.oss-rebuild.dev/hosts/Google",
		// TODO: Include build repository associated with this builder.
	}
	publicRebuildURI := path.Join("rebuild", buildInfo.Target.Artifact)
	// TODO: Change from "normalized" to "stabilized".
	publicNormalizedURI := path.Join("normalized", buildInfo.Target.Artifact)
	// Create comparison attestation.
	eqStmt := &in_toto.ProvenanceStatementSLSA1{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: buildInfo.Target.Artifact, Digest: makeDigestSet(up.Hash...)}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: slsa1.ProvenancePredicate{
			BuildDefinition: slsa1.ProvenanceBuildDefinition{
				BuildType: ArtifactEquivalenceBuildType,
				ExternalParameters: map[string]string{
					"candidate": publicRebuildURI,
					"target":    up.URI,
				},
				// NOTE: Could include comparison settings here when they're non-trivial.
				InternalParameters: nil,
				ResolvedDependencies: []slsa1.ResourceDescriptor{
					{Name: publicRebuildURI, Digest: makeDigestSet(rb.Hash...)},
					{Name: up.URI, Digest: makeDigestSet(up.Hash...)},
				},
			},
			RunDetails: slsa1.ProvenanceRunDetails{
				Builder: builder,
				BuildMetadata: slsa1.BuildMetadata{
					InvocationID: id,
				},
				Byproducts: []slsa1.ResourceDescriptor{
					{Name: publicNormalizedURI, Digest: makeDigestSet(up.StabilizedHash...)},
				},
			},
		},
	}
	var rd []slsa1.ResourceDescriptor
	inst, err := finalStrategy.GenerateFor(t, rebuild.BuildEnv{})
	if err != nil {
		return nil, nil, errors.Wrap(err, "retrieving repo")
	}
	if inst.Location.Ref != "" {
		rd = append(rd, slsa1.ResourceDescriptor{Name: "git+" + inst.Location.Repo, Digest: gitDigestSet(inst.Location)})
	}
	for n, s := range buildInfo.BuildImages {
		if !strings.HasPrefix(s, "sha256:") {
			return nil, nil, errors.New("buildInfo.BuildImages contains non-sha256 digest")
		}
		rd = append(rd, slsa1.ResourceDescriptor{Name: n, Digest: common.DigestSet{"sha256": strings.TrimPrefix(s, "sha256:")}})
	}
	// Empty the PullTiming and Status fields since they are superfluous to
	// downstream users.
	for _, s := range buildInfo.Steps {
		s.PullTiming = nil
		s.Status = ""
	}
	stepsBytes, err := json.Marshal(buildInfo.Steps)
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshalling GCB steps")
	}
	finalStrategyBytes, err := json.Marshal(schema.NewStrategyOneOf(finalStrategy))
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshalling Strategy")
	}
	externalParams := map[string]any{
		"ecosystem": string(t.Ecosystem),
		"package":   t.Package,
		"version":   t.Version,
		"artifact":  t.Artifact,
	}
	// Only add manual strategy field if it was used.
	if manualStrategy != nil {
		rawStrategy, err := json.Marshal(schema.NewStrategyOneOf(manualStrategy))
		if err != nil {
			return nil, nil, errors.Wrap(err, "marshalling manual strategy")
		}
		rd = append(rd, slsa1.ResourceDescriptor{Name: "build.fix.json", Content: rawStrategy})
		externalParams["buildConfigSource"] = map[string]string{
			"ref":        buildDef.Ref,
			"repository": buildDef.Repo,
			"path":       buildDef.Dir,
		}
	}
	stmt := &in_toto.ProvenanceStatementSLSA1{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: publicRebuildURI, Digest: makeDigestSet(rb.Hash...)}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: slsa1.ProvenancePredicate{
			BuildDefinition: slsa1.ProvenanceBuildDefinition{
				BuildType:            RebuildBuildType,
				ExternalParameters:   externalParams,
				ResolvedDependencies: rd,
				InternalParameters:   nil,
			},
			RunDetails: slsa1.ProvenanceRunDetails{
				Builder: builder,
				BuildMetadata: slsa1.BuildMetadata{
					InvocationID: id,
					StartedOn:    &buildInfo.BuildStart,
					FinishedOn:   &buildInfo.BuildEnd,
				},
				Byproducts: []slsa1.ResourceDescriptor{
					// NOTE: We use "build" externally instead of "strategy".
					{Name: "build.json", Content: finalStrategyBytes},
					{Name: "Dockerfile", Content: dockerfile},
					{Name: "steps.json", Content: stepsBytes},
				},
			},
		},
	}
	return eqStmt, stmt, nil
}

func checkClose(closer io.Closer) {
	if err := closer.Close(); err != nil {
		panic(errors.Wrap(err, "deferred close failed"))
	}
}
