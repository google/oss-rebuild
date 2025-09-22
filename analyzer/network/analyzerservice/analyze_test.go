// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/attestation"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/in-toto/in-toto-golang/in_toto"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
)

type FakeSigner struct{}

func (FakeSigner) Sign(ctx context.Context, data []byte) ([]byte, error) {
	return []byte("just trust me"), nil
}
func (FakeSigner) KeyID() (string, error) {
	return "fake", nil
}

type FakeVerifier struct{}

func (FakeVerifier) Verify(ctx context.Context, data, sig []byte) error {
	return nil
}
func (FakeVerifier) KeyID() (string, error) {
	return "fake", nil
}
func (FakeVerifier) Public() crypto.PublicKey {
	return nil
}

var _ dsse.Verifier = (*FakeVerifier)(nil)

func createMockAttestationBundle(t rebuild.Target, strategy rebuild.Strategy) []byte {
	strategyBytes := must(json.Marshal(schema.NewStrategyOneOf(strategy)))
	buildAttestation := &attestation.RebuildAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: "rebuild/" + t.Artifact, Digest: map[string]string{"sha256": "test-hash"}}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: attestation.RebuildPredicate{
			BuildDefinition: attestation.RebuildBuildDef{
				BuildType: attestation.BuildTypeRebuildV01,
				ExternalParameters: attestation.RebuildParams{
					Ecosystem: string(t.Ecosystem),
					Package:   t.Package,
					Version:   t.Version,
					Artifact:  t.Artifact,
				},
				InternalParameters: attestation.ServiceInternalParams{
					ServiceSource:  attestation.SourceLocation{Repository: "https://github.com/test/repo", Ref: "main"},
					PrebuildSource: attestation.SourceLocation{Repository: "https://github.com/test/prebuild", Ref: "v1.0.0"},
					PrebuildConfig: rebuild.PrebuildConfig{Bucket: "test-prebuild-bucket"},
				},
				ResolvedDependencies: attestation.RebuildDeps{
					Source: &slsa1.ResourceDescriptor{
						Name:   "git+https://github.com/test/source",
						Digest: map[string]string{"sha1": "abcd1234"},
					},
				},
			},
			RunDetails: attestation.RebuildRunDetails{
				Builder:       slsa1.Builder{ID: attestation.HostGoogle},
				BuildMetadata: slsa1.BuildMetadata{InvocationID: "test-id"},
				Byproducts: attestation.RebuildByproducts{
					BuildStrategy: slsa1.ResourceDescriptor{Name: attestation.ByproductBuildStrategy, Content: strategyBytes},
					Dockerfile:    slsa1.ResourceDescriptor{Name: attestation.ByproductDockerfile, Content: []byte("FROM scratch")},
					BuildSteps:    slsa1.ResourceDescriptor{Name: attestation.ByproductBuildSteps, Content: []byte("[]")},
				},
			},
		},
	}
	eqAttestation := &attestation.ArtifactEquivalenceAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: t.Artifact, Digest: map[string]string{"sha256": "test-hash"}}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: attestation.ArtifactEquivalencePredicate{
			BuildDefinition: attestation.ArtifactEquivalenceBuildDef{
				BuildType: attestation.BuildTypeArtifactEquivalenceV01,
				ExternalParameters: attestation.ArtifactEquivalenceParams{
					Candidate: "rebuild/" + t.Artifact,
					Target:    "https://upstream.example.com/" + t.Artifact,
				},
				InternalParameters: attestation.ServiceInternalParams{
					ServiceSource:  attestation.SourceLocation{Repository: "https://github.com/test/repo", Ref: "main"},
					PrebuildSource: attestation.SourceLocation{Repository: "https://github.com/test/prebuild", Ref: "v1.0.0"},
				},
			},
		},
	}
	var bundle bytes.Buffer
	encoder := json.NewEncoder(&bundle)
	buildEnvelope := &dsse.Envelope{
		PayloadType: attestation.InTotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(must(json.Marshal(must(buildAttestation.ToStatement())))),
		Signatures:  []dsse.Signature{{Sig: base64.StdEncoding.EncodeToString([]byte("mock-sig"))}},
	}
	eqEnvelope := &dsse.Envelope{
		PayloadType: attestation.InTotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(must(json.Marshal(must(eqAttestation.ToStatement())))),
		Signatures:  []dsse.Signature{{Sig: base64.StdEncoding.EncodeToString([]byte("mock-sig"))}},
	}
	must1(encoder.Encode(buildEnvelope))
	must1(encoder.Encode(eqEnvelope))
	return bundle.Bytes()
}

func TestAnalyze(t *testing.T) {
	for _, tc := range []struct {
		name        string
		target      rebuild.Target
		calls       []httpxtest.Call
		strategy    rebuild.Strategy
		file        *bytes.Buffer
		networkLog  string
		expectedMsg string
	}{
		{
			name:   "python wheel network analysis success",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "requests", Version: "2.31.0", Artifact: "requests-2.31.0-py3-none-any.whl"},
			calls: []httpxtest.Call{
				{
					URL: "https://pypi.org/pypi/requests/2.31.0/json",
					Response: &http.Response{
						StatusCode: 200,
						Body: httpxtest.Body(`{
							"info": {
								"name": "requests",
								"version": "2.31.0"
							},
							"urls": [
								{
									"filename": "requests-2.31.0-py3-none-any.whl",
									"url": "https://files.pythonhosted.org/packages/70/8e/0e2d847013cb52cd35b38c009bb167a1a26b2ce6cd6965bf26b47bc0bf44e/requests-2.31.0-py3-none-any.whl"
								}
							]
						}`),
					},
				},
				{
					URL: "https://files.pythonhosted.org/packages/70/8e/0e2d847013cb52cd35b38c009bb167a1a26b2ce6cd6965bf26b47bc0bf44e/requests-2.31.0-py3-none-any.whl",
					Response: &http.Response{
						StatusCode: 200,
						Body: io.NopCloser(must(archivetest.ZipFile([]archive.ZipEntry{
							{FileHeader: &zip.FileHeader{Name: "requests/__init__.py", Modified: time.UnixMilli(0)}, Body: []byte("# requests")},
						}))),
					},
				},
			},
			strategy: &pypi.PureWheelBuild{
				Location: rebuild.Location{Repo: "https://github.com/psf/requests", Ref: "aaaabbbbccccddddeeeeaaaabbbbccccddddeeee", Dir: "."},
			},
			file: must(archivetest.ZipFile([]archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "requests/__init__.py", Modified: time.UnixMilli(0)}, Body: []byte("# requests")},
			})),
			networkLog: `{"summary":{"totalRequests":3,"uniqueHosts":["pypi.org","files.pythonhosted.org","github.com"]}}`,
		},
		{
			name:   "analysis already exists",
			target: rebuild.Target{Ecosystem: rebuild.PyPI, Package: "existing", Version: "1.0.0", Artifact: "existing-1.0.0-py3-none-any.whl"},
			strategy: &pypi.PureWheelBuild{
				Location: rebuild.Location{Repo: "https://github.com/test/existing", Ref: "aaaabbbbccccddddeeeeaaaabbbbccccddddeeee", Dir: "."},
			},
			expectedMsg: "analysis already exists",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var d AnalyzerDeps
			d.HTTPClient = &httpxtest.MockClient{
				Calls:        tc.calls,
				URLValidator: httpxtest.NewURLValidator(t),
			}
			d.Signer = must(dsse.NewEnvelopeSigner(&FakeSigner{}))
			d.Verifier = must(dsse.NewEnvelopeVerifier(&FakeVerifier{}))
			// Set up filesystems
			mfs := memfs.New()
			inputAttestationFS := must(mfs.Chroot("input-attestations"))
			d.InputAttestationStore = rebuild.NewFilesystemAssetStore(inputAttestationFS)
			outputAnalysisFS := must(mfs.Chroot("output-analysis"))
			d.OutputAnalysisStore = rebuild.NewFilesystemAssetStore(outputAnalysisFS)
			d.LocalMetadataStore = rebuild.NewFilesystemAssetStore(must(mfs.Chroot("local-metadata")))
			d.DebugStoreBuilder = func(ctx context.Context) (rebuild.LocatableAssetStore, error) {
				return rebuild.NewFilesystemAssetStore(must(mfs.Chroot("debug-metadata"))), nil
			}
			remoteMetadata := rebuild.NewFilesystemAssetStore(must(mfs.Chroot("remote-metadata")))
			d.RemoteMetadataStoreBuilder = func(ctx context.Context, id string) (rebuild.LocatableAssetStore, error) {
				return remoteMetadata, nil
			}
			// Mock GCB client
			buildSteps := []*cloudbuild.BuildStep{
				{Name: "gcr.io/foo/bar", Script: "./bar"},
			}
			gcbclient := &gcbtest.MockClient{
				CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
					// Write rebuilt artifact
					if tc.file != nil {
						c := must(remoteMetadata.Writer(ctx, rebuild.RebuildAsset.For(tc.target)))
						defer c.Close()
						must(io.Copy(c, tc.file))
					}
					// Write network log
					if tc.networkLog != "" {
						netlogWriter := must(remoteMetadata.Writer(ctx, rebuild.ProxyNetlogAsset.For(tc.target)))
						defer netlogWriter.Close()
						must(netlogWriter.Write([]byte(tc.networkLog)))
					}
					// Write build steps
					buildInfoWriter := must(remoteMetadata.Writer(ctx, rebuild.BuildInfoAsset.For(tc.target)))
					defer buildInfoWriter.Close()
					must1(json.NewEncoder(buildInfoWriter).Encode(rebuild.BuildInfo{Steps: buildSteps}))

					return &cloudbuild.Operation{
						Name: "operations/build-id",
						Done: false,
						Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: &cloudbuild.Build{
							Id:     "build-id",
							Status: "QUEUED",
							Steps:  buildSteps,
						}})),
					}, nil
				},
				WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
					return &cloudbuild.Operation{
						Name: "operations/build-id",
						Done: true,
						Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: &cloudbuild.Build{
							Id:         "build-id",
							Status:     "SUCCESS",
							FinishTime: "2024-05-08T15:23:00Z",
							Steps:      buildSteps,
							Results:    &cloudbuild.Results{BuildStepImages: []string{"sha256:abcd"}},
						}})),
					}, nil
				},
			}
			d.GCBExecutor = must(buildgcb.NewExecutor(buildgcb.ExecutorConfig{
				Project:        "test-project",
				ServiceAccount: "test-service-account",
				LogsBucket:     "test-logs-bucket",
				Client:         gcbclient,
			}))
			d.ServiceRepo = rebuild.Location{Repo: "https://github.com/test/service", Ref: "main", Dir: "."}
			d.OverwriteAttestations = false
			// Setup input attestations
			bundleData := createMockAttestationBundle(tc.target, tc.strategy)
			bundleWriter := must(d.InputAttestationStore.Writer(ctx, rebuild.AttestationBundleAsset.For(tc.target)))
			defer bundleWriter.Close()
			must(bundleWriter.Write(bundleData))
			// Setup existing analysis for the "already exists" test case
			if tc.name == "analysis already exists" {
				existingAnalysis := must(d.OutputAnalysisStore.Writer(ctx, NetworkAnalysisAsset.For(tc.target)))
				defer existingAnalysis.Close()
				must(existingAnalysis.Write([]byte("existing analysis")))
			}
			// Execute the test
			result, err := Analyze(ctx, schema.AnalyzeRebuildRequest{
				Ecosystem: tc.target.Ecosystem,
				Package:   tc.target.Package,
				Version:   tc.target.Version,
				Artifact:  tc.target.Artifact,
			}, &d)

			if tc.expectedMsg != "" {
				if err == nil {
					t.Fatalf("Expected error containing %q, got nil", tc.expectedMsg)
				}
				if !strings.Contains(err.Error(), tc.expectedMsg) {
					t.Fatalf("Expected error containing %q, got %v", tc.expectedMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Analyze(): %v", err)
			}
			if result == nil {
				t.Fatal("Expected non-nil result")
			}
			// Verify network log was copied
			networkLogContent := string(must(io.ReadAll(must(d.OutputAnalysisStore.Reader(ctx, NetworkLogAsset.For(tc.target))))))
			if networkLogContent != tc.networkLog {
				t.Errorf("Network log content mismatch: want %q, got %q", tc.networkLog, networkLogContent)
			}
			// Verify analysis bundle was created
			bundleContent := must(io.ReadAll(must(d.OutputAnalysisStore.Reader(ctx, NetworkAnalysisAsset.For(tc.target)))))
			if len(bundleContent) == 0 {
				t.Error("Analysis bundle is empty")
			}
			// Parse and validate bundle contains expected attestations
			attestations := mustJSONL[map[string]any](bytes.NewReader(bundleContent))
			if len(attestations) != 2 {
				t.Errorf("Expected 2 attestations in bundle, got %d", len(attestations))
			}
		})
	}
}

func must[T any](t T, err error) T {
	must1(err)
	return t
}

func must1(err error) {
	if err != nil {
		panic(err)
	}
}

func mustJSONL[T any](r io.Reader) []T {
	var ts []T
	d := json.NewDecoder(r)
	for d.More() {
		var t T
		must1(d.Decode(&t))
		ts = append(ts, t)
	}
	return ts
}
