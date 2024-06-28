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

package rebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/cloudbuild/v1"
)

func TestRebuildContainerTpl(t *testing.T) {
	type testCase struct {
		name        string
		args        rebuildContainerArgs
		expected    string
		expectedErr bool
	}
	testCases := []testCase{
		{
			name: "Basic Usage",
			args: rebuildContainerArgs{
				Instructions: Instructions{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Source:     "git clone ...",
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
				UseTimewarp: false,
			},
			expected: `#syntax=docker/dockerfile:1.4
FROM alpine:3.19
RUN <<'EOF'
 set -eux
 apk add git make
 mkdir /src && cd /src
 git clone ...
 make deps ...
EOF
RUN cat <<'EOF' >build
 set -eux
 make build ...
 mkdir /out && cp /src/output/foo.tgz /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "With Timewarp",
			args: rebuildContainerArgs{
				Instructions: Instructions{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Source:     "git clone ...",
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
				UseTimewarp:        true,
				UtilPrebuildBucket: "my-bucket", // Add a bucket name
			},
			expected: `#syntax=docker/dockerfile:1.4
FROM gcr.io/cloud-builders/gsutil AS timewarp_provider
RUN gsutil cp -P gs://my-bucket/timewarp .
FROM alpine:3.19
COPY --from=timewarp_provider ./timewarp .
RUN <<'EOF'
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 apk add git make
 mkdir /src && cd /src
 git clone ...
 make deps ...
EOF
RUN cat <<'EOF' >build
 set -eux
 make build ...
 mkdir /out && cp /src/output/foo.tgz /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "Multi-Line Scripts",
			args: rebuildContainerArgs{
				Instructions: Instructions{
					Location:   Location{Repo: "my-repo", Ref: "dev", Dir: "/workspace"},
					SystemDeps: []string{"curl", "jq"},
					Source: `# Download source code
curl -LO https://example.com/source.tar.gz
tar xzf source.tar.gz`,
					Deps: `# Install dependencies
apk add --no-cache python3 py3-pip
pip install requests`,
					Build: `# Compile and package
python3 setup.py build
python3 setup.py sdist`,
					OutputPath: "dist/foo.whl",
				},
				UseTimewarp: false,
			},
			expected: `#syntax=docker/dockerfile:1.4
FROM alpine:3.19
RUN <<'EOF'
 set -eux
 apk add curl jq
 mkdir /src && cd /src
 # Download source code
 curl -LO https://example.com/source.tar.gz
 tar xzf source.tar.gz
 # Install dependencies
 apk add --no-cache python3 py3-pip
 pip install requests
EOF
RUN cat <<'EOF' >build
 set -eux
 # Compile and package
 python3 setup.py build
 python3 setup.py sdist
 mkdir /out && cp /src/dist/foo.whl /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := rebuildContainerTpl.Execute(&buf, tc.args)
			if (err != nil) != tc.expectedErr {
				t.Errorf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.expected, buf.String()); diff != "" {
				t.Errorf("Incorrect output (-want +got):\n%s", diff)
			}
		})
	}
}

// MockClient implements gcb.Client for testing.
type MockClient struct {
	CreateBuildFunc  func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	GetOperationFunc func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
}

func (mc *MockClient) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	return mc.CreateBuildFunc(ctx, project, build)
}

func (mc *MockClient) GetOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	return mc.GetOperationFunc(ctx, op)
}

func TestDoCloudBuild(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		beforeBuild := &cloudbuild.Build{
			Id:     "build-id",
			Status: "QUEUED",
			Steps: []*cloudbuild.BuildStep{
				{Name: "gcr.io/foo/bar", Script: "./bar"},
			},
		}
		afterBuild := &cloudbuild.Build{
			Id:         "build-id",
			Status:     "SUCCESS",
			FinishTime: "2024-05-08T15:23:00Z",
			Steps: []*cloudbuild.BuildStep{
				{Name: "gcr.io/foo/bar", Script: "./bar"},
			},
			Results: &cloudbuild.Results{BuildStepImages: []string{"sha256:abcd"}},
		}
		client := &MockClient{
			CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
				return &cloudbuild.Operation{
					Name:     "operations/build-id",
					Done:     false,
					Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: beforeBuild})),
				}, nil
			},
			GetOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
				return &cloudbuild.Operation{
					Name:     "operations/build-id",
					Done:     true,
					Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: afterBuild})),
				}, nil
			},
		}
		opts := RemoteOptions{Project: "test-project", LogsBucket: "test-logs-bucket", BuildServiceAccount: "test-service-account", UtilPrebuildBucket: "test-bootstrap"}
		target := Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"}
		bi := &BuildInfo{Target: target}
		err := doCloudBuild(context.Background(), client, beforeBuild, opts, bi)
		if err != nil {
		}
		expectedBI := &BuildInfo{
			Target:      target,
			BuildID:     "build-id",
			BuildEnd:    must(time.Parse(time.RFC3339, "2024-05-08T15:23:00Z")),
			Steps:       afterBuild.Steps,
			BuildImages: map[string]string{"gcr.io/foo/bar": "sha256:abcd"},
		}
		if diff := cmp.Diff(bi, expectedBI); diff != "" {
			t.Errorf("Unexpected BuildInfo: diff %v", diff)
		}
	})
}

func TestMakeBuild(t *testing.T) {
	dockerfile := "FROM alpine:3.19"
	imageUploadPath := "gs://test-bucket/image.tgz"
	rebuildUploadPath := "gs://test-bucket/pkg-version.tgz"
	opts := RemoteOptions{LogsBucket: "test-logs-bucket", BuildServiceAccount: "test-service-account", UtilPrebuildBucket: "test-bootstrap"}

	t.Run("Success", func(t *testing.T) {
		target := Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"}
		build := makeBuild(target, dockerfile, imageUploadPath, rebuildUploadPath, opts)
		diff := cmp.Diff(build, &cloudbuild.Build{
			LogsBucket:     "test-logs-bucket",
			Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
			ServiceAccount: "test-service-account",
			Steps: []*cloudbuild.BuildStep{
				{
					Name:   "gcr.io/cloud-builders/docker",
					Script: "cat <<'EOS' | docker buildx build --tag=img -\nFROM alpine:3.19\nEOS",
				},
				{
					Name: "gcr.io/cloud-builders/docker",
					Args: []string{"run", "--name=container", "img"},
				},
				{
					Name: "gcr.io/cloud-builders/docker",
					Args: []string{"cp", "container:/out/pkg-version.tgz", "/workspace/pkg-version.tgz"},
				},
				{
					Name:   "gcr.io/cloud-builders/docker",
					Script: "docker save img | gzip > /workspace/image.tgz",
				},
				{
					Name: "gcr.io/cloud-builders/gsutil",
					Script: ("" +
						"gsutil cp -P gs://test-bootstrap/gsutil_writeonly . && " +
						"./gsutil_writeonly cp /workspace/image.tgz gs://test-bucket/image.tgz && " +
						"./gsutil_writeonly cp /workspace/pkg-version.tgz gs://test-bucket/pkg-version.tgz"),
				},
			},
		})
		if diff != "" {
			t.Errorf("Unexpected Build: diff: %v", diff)
		}
	})
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
