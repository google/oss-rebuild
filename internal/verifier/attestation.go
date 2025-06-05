// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"

	"github.com/google/oss-rebuild/pkg/attestation"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/pkg/errors"
)

// CreateAttestations creates the SLSA attestations associated with a rebuild.
func CreateAttestations(ctx context.Context, t rebuild.Target, defn *schema.BuildDefinition, strategy rebuild.Strategy, id string, rb, up ArtifactSummary, metadata rebuild.AssetStore, serviceLoc, prebuildLoc, buildDefLoc rebuild.Location, prebuildConfig rebuild.PrebuildConfig) (equivalence, build *in_toto.ProvenanceStatementSLSA1, err error) {
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
		ID: attestation.HostGoogle,
	}
	internalParams := attestation.ServiceInternalParams{
		ServiceSource:  attestation.SourceLocationFromLocation(serviceLoc),
		PrebuildSource: attestation.SourceLocationFromLocation(prebuildLoc),
		PrebuildConfig: prebuildConfig,
	}
	publicRebuildURI := path.Join("rebuild", buildInfo.Target.Artifact)
	// TODO: Change from "normalized" to "stabilized".
	publicNormalizedURI := path.Join("normalized", buildInfo.Target.Artifact)
	// Create comparison attestation.
	eqStmt, err := (&attestation.ArtifactEquivalenceAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: buildInfo.Target.Artifact, Digest: makeDigestSet(up.Hash...)}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: attestation.ArtifactEquivalencePredicate{
			BuildDefinition: attestation.ArtifactEquivalenceBuildDef{
				BuildType: attestation.BuildTypeArtifactEquivalenceV01,
				ExternalParameters: attestation.ArtifactEquivalenceParams{
					Candidate: publicRebuildURI,
					Target:    up.URI,
				},
				InternalParameters: internalParams,
				ResolvedDependencies: attestation.ArtifactEquivalenceDeps{
					RebuiltArtifact:  slsa1.ResourceDescriptor{Name: publicRebuildURI, Digest: makeDigestSet(rb.Hash...)},
					UpstreamArtifact: slsa1.ResourceDescriptor{Name: up.URI, Digest: makeDigestSet(up.Hash...)},
				},
			},
			RunDetails: attestation.ArtifactEquivalenceRunDetails{
				Builder:       builder,
				BuildMetadata: slsa1.BuildMetadata{InvocationID: id},
				Byproducts: attestation.ArtifactEquivalenceByproducts{
					NormalizedArtifact: slsa1.ResourceDescriptor{Name: publicNormalizedURI, Digest: makeDigestSet(up.StabilizedHash...)},
				},
			},
		},
	}).ToStatement()
	if err != nil {
		return nil, nil, err
	}
	var deps attestation.RebuildDeps
	var loc rebuild.Location
	{
		// NOTE: Workaround the lack of a proper means of accessing Location on Strategy.
		// A timewarp host value is required to not break TimewarpURLFromString calls.
		inst, err := strategy.GenerateFor(t, rebuild.BuildEnv{TimewarpHost: "example.internal"})
		if err != nil {
			return nil, nil, errors.Wrap(err, "retrieving repo")
		}
		loc = inst.Location
	}
	if loc.Ref != "" {
		deps.Source = &slsa1.ResourceDescriptor{Name: "git+" + loc.Repo, Digest: GitDigestSet(loc)}
	}
	for n, s := range buildInfo.BuildImages {
		if !strings.HasPrefix(s, "sha256:") {
			return nil, nil, errors.New("buildInfo.BuildImages contains non-sha256 digest")
		}
		deps.Images = append(deps.Images, slsa1.ResourceDescriptor{Name: n, Digest: common.DigestSet{"sha256": strings.TrimPrefix(s, "sha256:")}})
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
	finalStrategyBytes, err := json.Marshal(schema.NewStrategyOneOf(strategy))
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshalling Strategy")
	}
	externalParams := attestation.RebuildParams{
		Ecosystem: string(t.Ecosystem),
		Package:   t.Package,
		Version:   t.Version,
		Artifact:  t.Artifact,
	}
	// Only add manual strategy field if it was used.
	if defn != nil {
		rawDefinition, err := json.Marshal(*defn)
		if err != nil {
			return nil, nil, errors.Wrap(err, "marshalling build definition")
		}
		deps.BuildFix = &slsa1.ResourceDescriptor{Name: attestation.DependencyBuildFix, Content: rawDefinition}
		src := attestation.SourceLocationFromLocation(buildDefLoc)
		externalParams.BuildConfigSource = &src
	}
	stmt, err := (&attestation.RebuildAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: publicRebuildURI, Digest: makeDigestSet(rb.Hash...)}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: attestation.RebuildPredicate{
			BuildDefinition: attestation.RebuildBuildDef{
				BuildType:            attestation.BuildTypeRebuildV01,
				ExternalParameters:   externalParams,
				InternalParameters:   internalParams,
				ResolvedDependencies: deps,
			},
			RunDetails: attestation.RebuildRunDetails{
				Builder: builder,
				BuildMetadata: slsa1.BuildMetadata{
					InvocationID: id,
					StartedOn:    &buildInfo.BuildStart,
					FinishedOn:   &buildInfo.BuildEnd,
				},
				Byproducts: attestation.RebuildByproducts{
					BuildStrategy: slsa1.ResourceDescriptor{Name: attestation.ByproductBuildStrategy, Content: finalStrategyBytes},
					Dockerfile:    slsa1.ResourceDescriptor{Name: attestation.ByproductDockerfile, Content: dockerfile},
					BuildSteps:    slsa1.ResourceDescriptor{Name: attestation.ByproductBuildSteps, Content: stepsBytes},
				},
			},
		},
	}).ToStatement()
	if err != nil {
		return nil, nil, err
	}
	return eqStmt, stmt, nil
}

func checkClose(closer io.Closer) {
	if err := closer.Close(); err != nil {
		panic(errors.Wrap(err, "deferred close failed"))
	}
}
