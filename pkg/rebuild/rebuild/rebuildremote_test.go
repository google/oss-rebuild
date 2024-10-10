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

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
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
		client := &gcbtest.MockClient{
			CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
				return &cloudbuild.Operation{
					Name:     "operations/build-id",
					Done:     false,
					Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: beforeBuild})),
				}, nil
			},
			WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
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

	type testCase struct {
		name        string
		target      Target
		dockerfile  string
		opts        RemoteOptions
		expected    *cloudbuild.Build
		expectedErr bool
	}
	testCases := []testCase{
		{
			name:       "standard build",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				RemoteMetadataStore: NewFilesystemAssetStore(memfs.New()),
			},
			expected: &cloudbuild.Build{
				LogsBucket:     "test-logs-bucket",
				Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
				ServiceAccount: "test-service-account",
				Steps: []*cloudbuild.BuildStep{
					{
						Name: "gcr.io/cloud-builders/docker",
						Script: `set -eux
cat <<'EOS' | docker buildx build --tag=img -
FROM alpine:3.19
EOS
docker run --name=container img
`,
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
						Script: `set -eux
gsutil cp -P gs://test-bootstrap/gsutil_writeonly .
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
`,
					},
				},
			},
		},
		{
			name:       "standard build with syscall monitoring",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				RemoteMetadataStore: NewFilesystemAssetStore(memfs.New()),
				UseSyscallMonitor:   true,
			},
			expected: &cloudbuild.Build{
				LogsBucket:     "test-logs-bucket",
				Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
				ServiceAccount: "test-service-account",
				Steps: []*cloudbuild.BuildStep{
					{
						Name: "gcr.io/cloud-builders/docker",
						Script: `set -eux
touch /workspace/tetragon.jsonl
docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --export-filename=/workspace/tetragon.jsonl
cat <<'EOS' | docker buildx build --tag=img -
FROM alpine:3.19
EOS
docker run --name=container img
docker kill tetragon
`,
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
						Script: `set -eux
gsutil cp -P gs://test-bootstrap/gsutil_writeonly .
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
./gsutil_writeonly cp /workspace/tetragon.jsonl file:///npm/pkg/version/pkg-version.tgz/tetragon.jsonl
`,
					},
				},
			},
		},
		{
			name:       "proxy build",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				RemoteMetadataStore: NewFilesystemAssetStore(memfs.New()),
				UseNetworkProxy:     true,
			},
			expected: &cloudbuild.Build{
				LogsBucket:     "test-logs-bucket",
				Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
				ServiceAccount: "test-service-account",
				Steps: []*cloudbuild.BuildStep{
					{
						Name: "gcr.io/cloud-builders/docker",
						Script: `set -eux
docker run --volume=/workspace:/workspace gcr.io/cloud-builders/gsutil cp -P gs://test-bootstrap/proxy /workspace
docker network create proxynet
useradd --system proxyu
uid=$(id -u proxyu)
docker run --detach --name=proxy --network=proxynet --privileged --volume=/workspace/proxy:/workspace/proxy --volume=/var/run/docker.sock:/var/run/docker.sock --entrypoint /bin/sh gcr.io/cloud-builders/docker -euxc '
	useradd --system --non-unique --uid '$uid' proxyu
	chown proxyu /workspace/proxy
	chown proxyu /var/run/docker.sock
	su - proxyu -c "/workspace/proxy \
		-verbose=true \
		-http_addr=:3128 \
		-tls_addr=:3129 \
		-ctrl_addr=:3127 \
		-docker_addr=:3130 \
		-docker_socket=/var/run/docker.sock \
		-docker_truststore_env_vars=PIP_CERT,CURL_CA_BUNDLE,NODE_EXTRA_CA_CERTS,CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE,NIX_SSL_CERT_FILE \
		-docker_network=container:build \
		-docker_java_truststore=true"
'
proxyIP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' proxy)
docker network connect cloudbuild proxy
docker run --detach --name=build --network=proxynet --entrypoint=/bin/sh gcr.io/cloud-builders/docker -c 'sleep infinity'
docker exec --privileged build /bin/sh -euxc '
	iptables -t nat -A OUTPUT -p tcp --dport 3128 -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp --dport 3129 -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp -m owner --uid-owner '$uid' -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp --dport 80 -j DNAT --to-destination '$proxyIP':3128
	iptables -t nat -A OUTPUT -p tcp --dport 443 -j DNAT --to-destination '$proxyIP':3129
'
docker exec build /bin/sh -euxc '
	curl http://proxy:3127/cert | tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt
	export DOCKER_HOST=tcp://proxy:3130 PROXYCERT=/etc/ssl/certs/proxy.crt
	docker buildx create --name proxied --bootstrap --driver docker-container --driver-opt network=container:build
	cat <<EOS | sed "s|^RUN|RUN --mount=type=bind,from=certs,dst=/etc/ssl/certs --mount=type=secret,id=PROXYCERT,env=PIP_CERT --mount=type=secret,id=PROXYCERT,env=CURL_CA_BUNDLE --mount=type=secret,id=PROXYCERT,env=NODE_EXTRA_CA_CERTS --mount=type=secret,id=PROXYCERT,env=CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE --mount=type=secret,id=PROXYCERT,env=NIX_SSL_CERT_FILE|" | \
		docker buildx build --builder proxied --build-context certs=/etc/ssl/certs --secret id=PROXYCERT --load --tag=img -
	FROM alpine:3.19
EOS
	docker run --name=container img
'
curl http://proxy:3127/summary > /workspace/netlog.json
`,
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
						Script: `set -eux
gsutil cp -P gs://test-bootstrap/gsutil_writeonly .
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
./gsutil_writeonly cp /workspace/netlog.json file:///npm/pkg/version/pkg-version.tgz/netlog.json
`,
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			build, err := makeBuild(tc.target, tc.dockerfile, tc.opts)
			if (err != nil) != tc.expectedErr {
				t.Errorf("Unexpected error: %v", err)
			} else if diff := cmp.Diff(build, tc.expected); diff != "" {
				t.Errorf("Unexpected Build: diff: %v", diff)
			}
		})
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
