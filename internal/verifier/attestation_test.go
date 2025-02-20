// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"google.golang.org/api/cloudbuild/v1"
)

func TestCreateAttestations(t *testing.T) {
	ctx := context.Background()
	target := rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "bytes", Version: "1.0.0", Artifact: "bytes-1.0.0.crate"}
	rbSummary := ArtifactSummary{
		URI:            "gs://rebuild.bucket/bytes-1.0.0.crate",
		Hash:           hashext.NewMultiHash(crypto.SHA256),
		StabilizedHash: hashext.NewMultiHash(crypto.SHA256),
	}
	upSummary := ArtifactSummary{
		URI:            "https://up.stream/bytes-1.0.0.crate",
		Hash:           hashext.NewMultiHash(crypto.SHA256),
		StabilizedHash: hashext.NewMultiHash(crypto.SHA256),
	}
	buildInfo := &rebuild.BuildInfo{
		Target:      target,
		BuildStart:  must(time.Parse(time.RFC3339, "2024-01-01T00:00:00Z")),
		BuildEnd:    must(time.Parse(time.RFC3339, "2024-01-01T00:00:00Z")),
		BuildImages: map[string]string{"gcr.io/foo/bar": "sha256:abcd"},
		Steps: []*cloudbuild.BuildStep{
			{
				Name:       "gcr.io/foo/bar",
				Script:     "./bar",
				Status:     "SUCCESS",
				PullTiming: &cloudbuild.TimeSpan{StartTime: "2024-01-01T00:00:00Z", EndTime: "2024-01-01T00:00:00Z"},
			},
		},
		BuildID: "build-id",
		Builder: "builder",
		ID:      "id",
	}

	t.Run("Success", func(t *testing.T) {
		// Set up in-memory filesystem
		fs := memfs.New()
		metadata := rebuild.NewFilesystemAssetStore(fs)
		{
			w := must(metadata.Writer(ctx, rebuild.DockerfileAsset.For(target)))
			must(w.Write([]byte("FROM alpine:latest\nRUN echo deps\nENTRYPOINT [\"echo\", \"build\"]")))
			orDie(w.Close())
		}
		{
			w := must(metadata.Writer(ctx, rebuild.BuildInfoAsset.For(target)))
			must(w.Write(must(json.Marshal(buildInfo))))
			orDie(w.Close())
		}
		inputStrategy := &rebuild.LocationHint{Location: rebuild.Location{Repo: "http://github.com/foo/bar", Ref: "0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33"}}
		strategy := &rebuild.ManualStrategy{Location: inputStrategy.Location, Deps: "echo deps", Build: "echo build", SystemDeps: []string{"git"}, OutputPath: "foo/bar"}
		input := rebuild.Input{Target: target, Strategy: inputStrategy}
		serviceLoc := rebuild.Location{Repo: "https://github.com/google/oss-rebuild", Ref: "v0.0.0-202501010000-feeddeadbeef00"}
		prebuildLoc := rebuild.Location{Repo: "https://github.com/google/oss-rebuild", Ref: "v0.0.0-202401010000-feeddeadbeef99"}
		buildDefLoc := rebuild.Location{Repo: "https://github.com/google/oss-rebuild", Ref: "b33eec7134eff8a16cb902b80e434de58bf37e2c", Dir: "definitions/cratesio/bytes/1.0.0/bytes-1.0.0.crate/build.yaml"}
		eqStmt, buildStmt, err := CreateAttestations(ctx, input, strategy, "test-id", rbSummary, upSummary, metadata, serviceLoc, prebuildLoc, buildDefLoc, &[]string{"pkg:foo/bar@baz"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		eqBytes := bytes.NewBuffer(nil)
		orDie(json.Indent(eqBytes, must(json.Marshal(eqStmt)), "", "  "))
		expectedEqStmt := `{
  "_type": "https://in-toto.io/Statement/v1",
  "predicateType": "https://slsa.dev/provenance/v1",
  "subject": [
    {
      "name": "bytes-1.0.0.crate",
      "digest": {
        "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
      }
    }
  ],
  "predicate": {
    "buildDefinition": {
      "buildType": "https://docs.oss-rebuild.dev/builds/ArtifactEquivalence@v0.1",
      "externalParameters": {
        "candidate": "rebuild/bytes-1.0.0.crate",
        "target": "https://up.stream/bytes-1.0.0.crate"
      },
      "internalParameters": {
        "prebuildSource": {
          "ref": "v0.0.0-202401010000-feeddeadbeef99",
          "repository": "https://github.com/google/oss-rebuild"
        },
        "serviceSource": {
          "ref": "v0.0.0-202501010000-feeddeadbeef00",
          "repository": "https://github.com/google/oss-rebuild"
        }
      },
      "resolvedDependencies": [
        {
          "digest": {
            "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
          },
          "name": "rebuild/bytes-1.0.0.crate"
        },
        {
          "digest": {
            "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
          },
          "name": "https://up.stream/bytes-1.0.0.crate"
        }
      ]
    },
    "runDetails": {
      "builder": {
        "id": "https://docs.oss-rebuild.dev/hosts/Google"
      },
      "metadata": {
        "invocationId": "test-id"
      },
      "byproducts": [
        {
          "digest": {
            "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
          },
          "name": "normalized/bytes-1.0.0.crate"
        }
      ]
    }
  }
}`
		if diff := cmp.Diff(eqBytes.String(), expectedEqStmt); diff != "" {
			t.Fatalf("Unexpected eqStmt: %v", diff)
		}
		buildBytes := bytes.NewBuffer(nil)
		orDie(json.Indent(buildBytes, must(json.Marshal(buildStmt)), "", "  "))
		expectedBuildStmt := `{
  "_type": "https://in-toto.io/Statement/v1",
  "predicateType": "https://slsa.dev/provenance/v1",
  "subject": [
    {
      "name": "rebuild/bytes-1.0.0.crate",
      "digest": {
        "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
      }
    }
  ],
  "predicate": {
    "buildDefinition": {
      "buildType": "https://docs.oss-rebuild.dev/builds/Rebuild@v0.1",
      "externalParameters": {
        "artifact": "bytes-1.0.0.crate",
        "buildConfigSource": {
          "path": "definitions/cratesio/bytes/1.0.0/bytes-1.0.0.crate/build.yaml",
          "ref": "b33eec7134eff8a16cb902b80e434de58bf37e2c",
          "repository": "https://github.com/google/oss-rebuild"
        },
        "ecosystem": "cratesio",
        "package": "bytes",
        "version": "1.0.0"
      },
      "internalParameters": {
        "prebuildSource": {
          "ref": "v0.0.0-202401010000-feeddeadbeef99",
          "repository": "https://github.com/google/oss-rebuild"
        },
        "serviceSource": {
          "ref": "v0.0.0-202501010000-feeddeadbeef00",
          "repository": "https://github.com/google/oss-rebuild"
        }
      },
      "resolvedDependencies": [
        {
          "digest": {
            "sha1": "0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33"
          },
          "name": "git+http://github.com/foo/bar"
        },
        {
          "digest": {
            "sha256": "abcd"
          },
          "name": "gcr.io/foo/bar"
        },
        {
          "name": "build.fix.json",
          "content": "eyJyZWJ1aWxkX2xvY2F0aW9uX2hpbnQiOnsicmVwbyI6Imh0dHA6Ly9naXRodWIuY29tL2Zvby9iYXIiLCJyZWYiOiIwYmVlYzdiNWVhM2YwZmRiYzk1ZDBkZDQ3ZjNjNWJjMjc1ZGE4YTMzIiwiZGlyIjoiIn19"
        }
      ]
    },
    "runDetails": {
      "builder": {
        "id": "https://docs.oss-rebuild.dev/hosts/Google"
      },
      "metadata": {
        "invocationId": "test-id",
        "startedOn": "2024-01-01T00:00:00Z",
        "finishedOn": "2024-01-01T00:00:00Z"
      },
      "byproducts": [
        {
          "name": "build.json",
          "content": "eyJtYW51YWwiOnsicmVwbyI6Imh0dHA6Ly9naXRodWIuY29tL2Zvby9iYXIiLCJyZWYiOiIwYmVlYzdiNWVhM2YwZmRiYzk1ZDBkZDQ3ZjNjNWJjMjc1ZGE4YTMzIiwiZGlyIjoiIiwiZGVwcyI6ImVjaG8gZGVwcyIsImJ1aWxkIjoiZWNobyBidWlsZCIsInN5c3RlbV9kZXBzIjpbImdpdCJdLCJvdXRwdXRfcGF0aCI6ImZvby9iYXIifX0="
        },
        {
          "name": "Dockerfile",
          "content": "RlJPTSBhbHBpbmU6bGF0ZXN0ClJVTiBlY2hvIGRlcHMKRU5UUllQT0lOVCBbImVjaG8iLCAiYnVpbGQiXQ=="
        },
        {
          "name": "steps.json",
          "content": "W3sibmFtZSI6Imdjci5pby9mb28vYmFyIiwic2NyaXB0IjoiLi9iYXIifV0="
        },
        {
          "name": "urls.json",
          "content": "WyJwa2c6Zm9vL2JhckBiYXoiXQ=="
        }
      ]
    }
  }
}`
		if diff := cmp.Diff(buildBytes.String(), expectedBuildStmt); diff != "" {
			t.Fatalf("Unexpected buildStmt: %v", diff)
		}
	})
}
