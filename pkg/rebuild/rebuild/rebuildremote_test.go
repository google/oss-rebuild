// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"google.golang.org/api/cloudbuild/v1"
)

func TestMakeDockerfile(t *testing.T) {
	type testCase struct {
		name        string
		input       Input
		opts        RemoteOptions
		expected    string
		expectedErr bool
	}
	testCases := []testCase{
		{
			name: "Basic Usage",
			input: Input{
				Target: Target{},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: RemoteOptions{
				UseTimewarp: false,
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<'EOF'
 set -eux
 apk add git make
EOF
RUN <<'EOF'
 set -eux
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN cat <<'EOF' >/build
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
			input: Input{
				Target: Target{},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: RemoteOptions{
				UseTimewarp:        true,
				UtilPrebuildBucket: "my-bucket",
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<'EOF'
 set -eux
 wget https://my-bucket.storage.googleapis.com/timewarp
 chmod +x timewarp
 apk add git make
EOF
RUN <<'EOF'
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN cat <<'EOF' >/build
 set -eux
 make build ...
 mkdir /out && cp /src/output/foo.tgz /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "With Timewarp and auth",
			input: Input{
				Target: Target{},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: RemoteOptions{
				UseTimewarp:        true,
				UtilPrebuildBucket: "my-bucket",
				UtilPrebuildAuth:   true,
				Project:            "my-project",
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN --mount=type=secret,id=auth_header <<'EOF'
 set -eux
 apk add curl && curl -O -H @/run/secrets/auth_header https://my-bucket.storage.googleapis.com/timewarp
 chmod +x timewarp
 apk add git make
EOF
RUN <<'EOF'
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN cat <<'EOF' >/build
 set -eux
 make build ...
 mkdir /out && cp /src/output/foo.tgz /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "With Timewarp at a Subdir",
			input: Input{
				Target: Target{},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: RemoteOptions{
				UseTimewarp:        true,
				UtilPrebuildBucket: "my-bucket",
				UtilPrebuildDir:    "v0.0.0-202501010000-feeddeadbeef00",
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<'EOF'
 set -eux
 wget https://my-bucket.storage.googleapis.com/v0.0.0-202501010000-feeddeadbeef00/timewarp
 chmod +x timewarp
 apk add git make
EOF
RUN <<'EOF'
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN cat <<'EOF' >/build
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
			input: Input{
				Target: Target{},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"curl", "jq"},
					Deps: `# Install dependencies
apk add --no-cache python3 py3-pip
pip install requests`,
					Build: `# Compile and package
python3 setup.py build
python3 setup.py sdist`,
					OutputPath: "dist/foo.whl",
				},
			},
			opts: RemoteOptions{
				UseTimewarp: false,
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<'EOF'
 set -eux
 apk add curl jq git
EOF
RUN <<'EOF'
 set -eux
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 # Install dependencies
 apk add --no-cache python3 py3-pip
 pip install requests
EOF
RUN cat <<'EOF' >/build
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
		{
			name: "Debian",
			input: Input{
				Target: Target{
					Ecosystem: Debian,
				},
				Strategy: &ManualStrategy{
					Location:   Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					SystemDeps: []string{"git", "make"},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: RemoteOptions{
				UseTimewarp: false,
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/debian:trixie-20250203-slim
RUN <<'EOF'
 set -eux
 apt update
 apt install -y git make
EOF
RUN <<'EOF'
 set -eux
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN cat <<'EOF' >/build
 set -eux
 make build ...
 ls
 ls /src/
 mkdir /out && cp /src/output/foo.tgz /out/
EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := MakeDockerfile(tc.input, tc.opts)
			if (err != nil) != tc.expectedErr {
				t.Errorf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.expected, actual); diff != "" {
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
			CancelOperationFunc: func(op *cloudbuild.Operation) error { return nil },
		}
		opts := RemoteOptions{Project: "test-project", LogsBucket: "test-logs-bucket", BuildServiceAccount: "test-service-account", UtilPrebuildBucket: "test-bootstrap"}
		target := Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"}
		bi := &BuildInfo{Target: target}
		err := doCloudBuild(context.Background(), client, beforeBuild, opts, bi)
		if err != nil {
			t.Errorf("Unexpected doCLoudBuildError %v", err)
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
			dockerfile: "FROM docker.io/library/alpine:3.19",
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
						Script: `#!/usr/bin/env bash
set -eux
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
cat <<'EOS' | docker buildx build --tag=img -
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
wget https://test-bootstrap.storage.googleapis.com/gsutil_writeonly
chmod +x gsutil_writeonly
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
			dockerfile: "FROM docker.io/library/alpine:3.19",
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
						Script: `#!/usr/bin/env bash
set -eux
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
touch /workspace/tetragon.jsonl
mkdir /workspace/tetragon/
echo '{"apiVersion":"cilium.io/v1alpha1","kind":"TracingPolicy","metadata":{"name":"process-and-memory"},"spec":{"kprobes":[{"args":[{"index":0,"type":"file"},{"index":1,"type":"int"}],"call":"security_file_permission","return":true,"returnArg":{"index":0,"type":"int"},"returnArgAction":"Post","syscall":false},{"args":[{"index":0,"type":"file"},{"index":1,"type":"uint64"},{"index":2,"type":"uint32"}],"call":"security_mmap_file","return":true,"returnArg":{"index":0,"type":"int"},"returnArgAction":"Post","syscall":false},{"args":[{"index":0,"type":"path"}],"call":"security_path_truncate","return":true,"returnArg":{"index":0,"type":"int"},"returnArgAction":"Post","syscall":false}]}}' > "/workspace/tetragon/policy_0.json"
echo '{"apiVersion":"cilium.io/v1alpha1","kind":"TracingPolicy","metadata":{"name":"file-open-at"},"spec":{"tracepoints":[{"args":[{"index":5,"type":"int32"},{"index":6,"type":"string"},{"index":7,"type":"uint32"},{"index":8,"type":"uint32"}],"event":"sys_enter_openat","subsystem":"syscalls"}]}}' > "/workspace/tetragon/policy_1.json"
export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/workspace/tetragon/:/workspace/tetragon/ -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --tracing-policy-dir=/workspace/tetragon/ --export-filename=/workspace/tetragon.jsonl)
grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
cat <<'EOS' | docker buildx build --tag=img -
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
wget https://test-bootstrap.storage.googleapis.com/gsutil_writeonly
chmod +x gsutil_writeonly
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
./gsutil_writeonly cp /workspace/tetragon.jsonl file:///npm/pkg/version/pkg-version.tgz/tetragon.jsonl
`,
					},
				},
			},
		},
		{
			name:       "standard build with auth",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM docker.io/library/alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				RemoteMetadataStore: NewFilesystemAssetStore(memfs.New()),
				UtilPrebuildAuth:    true,
				Project:             "test-project",
			},
			expected: &cloudbuild.Build{
				LogsBucket:     "test-logs-bucket",
				Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
				ServiceAccount: "test-service-account",
				Steps: []*cloudbuild.BuildStep{
					{
						Name: "gcr.io/cloud-builders/docker",
						Script: `#!/usr/bin/env bash
set -eux
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@test-project.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
cat <<'EOS' | docker buildx build --secret id=auth_header,src=/tmp/auth_header --tag=img -
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
apk add curl jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@test-project.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
curl -O -H @/tmp/auth_header https://test-bootstrap.storage.googleapis.com/gsutil_writeonly
chmod +x gsutil_writeonly
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
`,
					},
				},
			},
		},
		{
			name:       "proxy build",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM docker.io/library/alpine:3.19",
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
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
curl -O https://test-bootstrap.storage.googleapis.com/proxy
chmod +x proxy
docker network create proxynet
useradd --system proxyu
uid=$(id -u proxyu)
docker run --detach --name=proxy --network=proxynet --privileged -v=/workspace/proxy:/workspace/proxy -v=/var/run/docker.sock:/var/run/docker.sock --entrypoint /bin/sh gcr.io/cloud-builders/docker -euxc '
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
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
wget https://test-bootstrap.storage.googleapis.com/gsutil_writeonly
chmod +x gsutil_writeonly
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
./gsutil_writeonly cp /workspace/netlog.json file:///npm/pkg/version/pkg-version.tgz/netlog.json
`,
					},
				},
			},
		},
		{
			name:       "proxy build at subdir",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM docker.io/library/alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				UtilPrebuildDir:     "v0.0.0-202501010000-feeddeadbeef00",
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
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
curl -O https://test-bootstrap.storage.googleapis.com/v0.0.0-202501010000-feeddeadbeef00/proxy
chmod +x proxy
docker network create proxynet
useradd --system proxyu
uid=$(id -u proxyu)
docker run --detach --name=proxy --network=proxynet --privileged -v=/workspace/proxy:/workspace/proxy -v=/var/run/docker.sock:/var/run/docker.sock --entrypoint /bin/sh gcr.io/cloud-builders/docker -euxc '
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
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
wget https://test-bootstrap.storage.googleapis.com/v0.0.0-202501010000-feeddeadbeef00/gsutil_writeonly
chmod +x gsutil_writeonly
./gsutil_writeonly cp /workspace/image.tgz file:///npm/pkg/version/pkg-version.tgz/image.tgz
./gsutil_writeonly cp /workspace/pkg-version.tgz file:///npm/pkg/version/pkg-version.tgz/pkg-version.tgz
./gsutil_writeonly cp /workspace/netlog.json file:///npm/pkg/version/pkg-version.tgz/netlog.json
`,
					},
				},
			},
		},
		{
			name:       "proxy build with auth",
			target:     Target{Ecosystem: NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			dockerfile: "FROM docker.io/library/alpine:3.19",
			opts: RemoteOptions{
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "test-service-account",
				UtilPrebuildBucket:  "test-bootstrap",
				RemoteMetadataStore: NewFilesystemAssetStore(memfs.New()),
				UseNetworkProxy:     true,
				UtilPrebuildAuth:    true,
				Project:             "test-project",
			},
			expected: &cloudbuild.Build{
				LogsBucket:     "test-logs-bucket",
				Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
				ServiceAccount: "test-service-account",
				Steps: []*cloudbuild.BuildStep{
					{
						Name: "gcr.io/cloud-builders/docker",
						Script: `set -eux
echo 'Starting rebuild for {Ecosystem:npm Package:pkg Version:version Artifact:pkg-version.tgz}'
apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@test-project.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
curl -O -H @/tmp/auth_header https://test-bootstrap.storage.googleapis.com/proxy
chmod +x proxy
docker network create proxynet
useradd --system proxyu
uid=$(id -u proxyu)
docker run --detach --name=proxy --network=proxynet --privileged -v=/workspace/proxy:/workspace/proxy -v=/var/run/docker.sock:/var/run/docker.sock --entrypoint /bin/sh gcr.io/cloud-builders/docker -euxc '
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
(printf 'HEADER='; cat /tmp/auth_header) > /tmp/envfile
docker run --detach --name=build --network=proxynet --entrypoint=/bin/sh --env-file /tmp/envfile gcr.io/cloud-builders/docker -c 'sleep infinity'
docker exec --privileged build /bin/sh -euxc '
	iptables -t nat -A OUTPUT -p tcp --dport 3128 -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp --dport 3129 -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp -m owner --uid-owner '$uid' -j ACCEPT
	iptables -t nat -A OUTPUT -p tcp --dport 80 -j DNAT --to-destination '$proxyIP':3128
	iptables -t nat -A OUTPUT -p tcp --dport 443 -j DNAT --to-destination '$proxyIP':3129
'
docker exec build /bin/sh -euxc '
	curl http://proxy:3127/cert | tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt
	export DOCKER_HOST=tcp://proxy:3130 PROXYCERT=/etc/ssl/certs/proxy.crt HEADER
	docker buildx create --name proxied --bootstrap --driver docker-container --driver-opt network=container:build
	cat <<EOS | sed "s|^RUN|RUN --mount=type=bind,from=certs,dst=/etc/ssl/certs --mount=type=secret,id=PROXYCERT,env=PIP_CERT --mount=type=secret,id=PROXYCERT,env=CURL_CA_BUNDLE --mount=type=secret,id=PROXYCERT,env=NODE_EXTRA_CA_CERTS --mount=type=secret,id=PROXYCERT,env=CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE --mount=type=secret,id=PROXYCERT,env=NIX_SSL_CERT_FILE|" | \
		docker buildx build --builder proxied --build-context certs=/etc/ssl/certs --secret id=PROXYCERT --secret id=auth_header,env=HEADER --load --tag=img -
FROM docker.io/library/alpine:3.19
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
						Name: "docker.io/library/alpine:3.19",
						Script: `set -eux
apk add curl jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@test-project.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
curl -O -H @/tmp/auth_header https://test-bootstrap.storage.googleapis.com/gsutil_writeonly
chmod +x gsutil_writeonly
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
