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
	"io"
	"log"
	"os"
	"path"
	"strings"
	"text/template"
	"time"

	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"gopkg.in/yaml.v3"
)

// RemoteOptions provides the configuration to execute rebuilds on Cloud Build.
type RemoteOptions struct {
	GCBClient           gcb.Client
	Project             string
	BuildServiceAccount string
	LogsBucket          string
	// LocalMetadataStore stores the dockerfile and build info. Cloud build does not need access to this.
	LocalMetadataStore AssetStore
	// DebugStore is the durable storage of the dockerfile and build info. Cloud build does not need access to this. It should be keyed by RunID to allow programatic access.
	DebugStore AssetStore
	// RemoteMetadataStore stores the rebuilt artifact. Cloud build needs access to upload assets here. It should be keyed by the unguessable UUID to sandbox each build.
	RemoteMetadataStore LocatableAssetStore
	UtilPrebuildBucket  string
	// TODO: Consider moving these to Strategy.
	UseTimewarp       bool
	UseNetworkProxy   bool
	UseSyscallMonitor bool
}

type rebuildContainerArgs struct {
	Instructions
	UseTimewarp        bool
	UseNetworkProxy    bool
	UtilPrebuildBucket string
}

const policyYaml = `
apiVersion: cilium.io/v1alpha1
kind: TracingPolicy
metadata:
  name: "process-and-memory"
spec:
  kprobes:
  - call: "security_file_permission"
    syscall: false
    return: true
    args:
    - index: 0
      type: "file" # (struct file *) used for getting the path
    - index: 1
      type: "int" # 0x04 is MAY_READ, 0x02 is MAY_WRITE
    returnArg:
      index: 0
      type: "int"
    returnArgAction: "Post"
  - call: "security_mmap_file"
    syscall: false
    return: true
    args:
    - index: 0
      type: "file" # (struct file *) used for getting the path
    - index: 1
      type: "uint64" # the prot flags PROT_READ(0x01), PROT_WRITE(0x02), PROT_EXEC(0x04)
    - index: 2
      type: "uint32" # the mmap flags (i.e. MAP_SHARED, ...)
    returnArg:
      index: 0
      type: "int"
    returnArgAction: "Post"
  - call: "security_path_truncate"
    syscall: false
    return: true
    args:
    - index: 0
      type: "path" # (struct path *) used for getting the path
    returnArg:
      index: 0
      type: "int"
    returnArgAction: "Post"
`

var tetragonPolicyJSON string

func init() {
	var data any
	if err := yaml.Unmarshal([]byte(policyYaml), &data); err != nil {
		log.Fatalf("Malformed tetragon policy: %v", err)
	}
	b, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("Converting tetragon policy to json: %v", err)
	}
	tetragonPolicyJSON = string(b)
}

var debuildContainerTpl = template.Must(
	template.New(
		"rebuild container",
	).Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n ") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		// NOTE: For syntax docs, see https://docs.docker.com/build/dockerfile/release-notes/
		// TODO: Find a base image that has build-essentials installed, that would improve startup time significantly, and it would pin the build tools we're using.
		textwrap.Dedent(`
				#syntax=docker/dockerfile:1.4
				FROM docker.io/library/debian:bookworm-20240211-slim
				RUN <<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 curl https://{{.UtilPrebuildBucket}}.storage.googleapis.com/timewarp > timewarp
				 chmod +x timewarp
				 ./timewarp -port 8080 &
				 while ! nc -z localhost 8080;do sleep 1;done
				{{- end}}
				 apt update
				 apt install -y {{join " " .Instructions.SystemDeps}}
				EOF
				RUN <<'EOF'
				 set -eux
				 mkdir /src && cd /src
				 {{.Instructions.Source| indent}}
				 {{.Instructions.Deps | indent}}
				EOF
				RUN cat <<'EOF' >build
				 set -eux
				 {{.Instructions.Build | indent}}
				 ls
				 ls /src/
				 mkdir /out && cp /src/{{.Instructions.OutputPath}} /out/
				EOF
				WORKDIR "/src"
				ENTRYPOINT ["/bin/sh","/build"]
				`)[1:], // remove leading newline
	))

var alpineContainerTpl = template.Must(
	template.New(
		"rebuild container",
	).Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n ") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		// NOTE: For syntax docs, see https://docs.docker.com/build/dockerfile/release-notes/
		textwrap.Dedent(`
				#syntax=docker/dockerfile:1.4
				FROM docker.io/library/alpine:3.19
				RUN <<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 wget https://{{.UtilPrebuildBucket}}.storage.googleapis.com/timewarp
				 chmod +x timewarp
				 ./timewarp -port 8080 &
				 while ! nc -z localhost 8080;do sleep 1;done
				{{- end}}
				 apk add {{join " " .Instructions.SystemDeps}}
				EOF
				RUN <<'EOF'
				 set -eux
				 mkdir /src && cd /src
				 {{.Instructions.Source| indent}}
				 {{.Instructions.Deps | indent}}
				EOF
				RUN cat <<'EOF' >build
				 set -eux
				 {{.Instructions.Build | indent}}
				 mkdir /out && cp /src/{{.Instructions.OutputPath}} /out/
				EOF
				WORKDIR "/src"
				ENTRYPOINT ["/bin/sh","/build"]
				`)[1:], // remove leading newline
	))

var standardBuildTpl = template.Must(
	template.New(
		"standard build",
	).Parse(
		textwrap.Dedent(`
				#!/usr/bin/env bash
				set -eux
				{{- if .UseSyscallMonitor}}
				touch /workspace/tetragon.jsonl
				echo '{{.SyscallPolicy}}' > /workspace/tetragon_policy.yaml
				export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/workspace/tetragon_policy.yaml:/workspace/tetragon_policy.yaml -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --tracing-policy=/workspace/tetragon_policy.yaml --export-filename=/workspace/tetragon.jsonl)
				grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
				{{- end}}
				cat <<'EOS' | docker buildx build --tag=img -
				{{.Dockerfile}}
				EOS
				docker run --name=container img
				{{- if .UseSyscallMonitor}}
				docker kill tetragon
				{{- end}}
				`)[1:], // remove leading newline
	))

// NOTE(impl): There are a number of factors complicating this harness that warrant some explanation.
//   - Overview: The proxy and build are executed in separate containers. All
//     HTTP traffic is redirected to the proxy from the build AND containers
//     created from the build (i.e. buildx workers).
//   - Network basics: The top-level container uses the pre-allocated
//     "cloudbuild" network on GCB. We create a separate "proxynet" network to
//     bridge the build and the proxy. The proxy is also connected to the
//     "cloudbuild" network so its admin server is accessible from the
//     top-level container to retrieve the network log.
//   - Network namespaces: We use iptables to redirect traffic to the proxy.
//     iptables is configured per-network namespace. Crucially, Docker networks
//     are not the same as network namespaces! By default, though, each
//     container is allocated its own namespace even if they're connected to
//     the same network. So to apply iptables rules to the build container, we
//     need to ensure they all execute the iptables rules (which requires root)
//     in that same container. But to ensure the same for all derivative
//     containers of the build, we need those same iptables rules. Luckily,
//     Docker does allow you to specify "container:<NAME>" as the container
//     network which joins both the same network AND the same network
//     namespace. So we use the proxy to enforce that all new containers use
//     the "container:build" network.
//   - User setup: We use the iptables' owner module feature of redirecting
//     traffic based on the UID of the originating process. To do so here,
//     though, we need a user/uid that's shared across the proxy and build
//     containers. The user namespaces are not shared between the containers so
//     we cannot refer to the user by name from one if it's created by the
//     other. Creating a user at the top-level, recreating it in the proxy, and
//     using the uid in the build satisfies our constraints.
//   - Docker socket access: For the proxy to read and write to the docker
//     socket, it needs to be part of the owning user (root) or group
//     (host-defined "docker" group unknown within the container). Associating
//     the proxy with a shadowed "docker" group does not seem to work. Changing
//     ownership of the socket resolves this, albeit suboptimally.
//   - Docker build: To ensure the proxy cert is trusted during the build, each
//     execution (i.e. RUN instruction) must be patched to mount the proxy
//     certificate and add truststore env vars. This is currently done
//     lexically on the input Dockerfile but could be modified to operate on
//     the lower-level build representation.
//
// TODO: Support IPv6.
var proxyBuildTpl = template.Must(
	template.New(
		"proxy build",
	).Funcs(template.FuncMap{
		"join": func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		textwrap.Dedent(`
				set -eux
				curl -O https://{{.UtilPrebuildBucket}}.storage.googleapis.com/proxy
				chmod +x proxy
				docker network create proxynet
				useradd --system {{.User}}
				uid=$(id -u {{.User}})
				docker run --detach --name=proxy --network=proxynet --privileged -v=/workspace/proxy:/workspace/proxy -v=/var/run/docker.sock:/var/run/docker.sock --entrypoint /bin/sh gcr.io/cloud-builders/docker -euxc '
					useradd --system --non-unique --uid '$uid' {{.User}}
					chown {{.User}} /workspace/proxy
					chown {{.User}} /var/run/docker.sock
					su - {{.User}} -c "/workspace/proxy \
						-verbose=true \
						-http_addr=:{{.HTTPPort}} \
						-tls_addr=:{{.TLSPort}} \
						-ctrl_addr=:{{.CtrlPort}} \
						-docker_addr=:{{.DockerPort}} \
						-docker_socket=/var/run/docker.sock \
						-docker_truststore_env_vars={{join "," .CertEnvVars}} \
						-docker_network=container:build \
						-docker_java_truststore=true"
				'
				proxyIP=$(docker inspect -f '{{printf "%s" "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}"}}' proxy)
				docker network connect cloudbuild proxy
				docker run --detach --name=build --network=proxynet --entrypoint=/bin/sh gcr.io/cloud-builders/docker -c 'sleep infinity'
				docker exec --privileged build /bin/sh -euxc '
					iptables -t nat -A OUTPUT -p tcp --dport {{.HTTPPort}} -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp --dport {{.TLSPort}} -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp -m owner --uid-owner '$uid' -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp --dport 80 -j DNAT --to-destination '$proxyIP':{{.HTTPPort}}
					iptables -t nat -A OUTPUT -p tcp --dport 443 -j DNAT --to-destination '$proxyIP':{{.TLSPort}}
				'
				{{- if .UseSyscallMonitor}}
				touch /workspace/tetragon.jsonl
				echo {{.SyscallPolicy}} > /workspace/tetragon_policy.yaml
				export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --policy-file=/workspace/tetragon_policy.yaml --export-filename=/workspace/tetragon.jsonl)
				grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
				{{- end}}
				docker exec build /bin/sh -euxc '
					curl http://proxy:{{.CtrlPort}}/cert | tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt
					export DOCKER_HOST=tcp://proxy:{{.DockerPort}} PROXYCERT=/etc/ssl/certs/proxy.crt
					docker buildx create --name proxied --bootstrap --driver docker-container --driver-opt network=container:build
					cat <<EOS | sed "s|^RUN|RUN --mount=type=bind,from=certs,dst=/etc/ssl/certs{{range .CertEnvVars}} --mount=type=secret,id=PROXYCERT,env={{.}}{{end}}|" | \
						docker buildx build --builder proxied --build-context certs=/etc/ssl/certs --secret id=PROXYCERT --load --tag=img -
					{{.Dockerfile}}
				EOS
					docker run --name=container img
				'
				{{- if .UseSyscallMonitor}}
				docker kill tetragon
				{{- end}}
				curl http://proxy:{{.CtrlPort}}/summary > /workspace/netlog.json
				`)[1:], // remove leading newline
	))

type upload struct {
	From string
	To   string
}

var assetUploadTpl = template.Must(
	template.New(
		"asset upload",
	).Parse(
		textwrap.Dedent(`
				set -eux
				wget https://{{.UtilPrebuildBucket}}.storage.googleapis.com/gsutil_writeonly
				chmod +x gsutil_writeonly
				{{- range .Uploads}}
				./gsutil_writeonly cp {{.From}} {{.To}}
				{{- end}}
				`)[1:], // remove leading newline
	))

func makeBuild(t Target, dockerfile string, opts RemoteOptions) (*cloudbuild.Build, error) {
	var buildScript bytes.Buffer
	uploads := []upload{
		{From: "/workspace/image.tgz", To: opts.RemoteMetadataStore.URL(Asset{Target: t, Type: ContainerImageAsset}).String()},
		{From: path.Join("/workspace", t.Artifact), To: opts.RemoteMetadataStore.URL(Asset{Target: t, Type: RebuildAsset}).String()},
	}
	if opts.UseSyscallMonitor {
		uploads = append(uploads, upload{From: "/workspace/tetragon.jsonl", To: opts.RemoteMetadataStore.URL(Asset{Target: t, Type: TetragonLogAsset}).String()})
	}
	if opts.UseNetworkProxy {
		err := proxyBuildTpl.Execute(&buildScript, map[string]any{
			"UtilPrebuildBucket": opts.UtilPrebuildBucket,
			"Dockerfile":         dockerfile,
			"UseSyscallMonitor":  opts.UseSyscallMonitor,
			"SyscallPolicy":      tetragonPolicyJSON,
			"HTTPPort":           "3128",
			"TLSPort":            "3129",
			"CtrlPort":           "3127",
			"DockerPort":         "3130",
			"User":               "proxyu",
			"CertEnvVars": []string{
				// Used by pip.
				// See https://pip.pypa.io/en/stable/topics/https-certificates/#using-a-specific-certificate-store
				"PIP_CERT",
				// Used by curl.
				// See https://curl.se/docs/sslcerts.html#use-a-custom-ca-store
				"CURL_CA_BUNDLE",
				// Used by npm and node.
				// See https://nodejs.org/api/cli.html#node_extra_ca_certsfile
				"NODE_EXTRA_CA_CERTS",
				// Used by gcloud.
				// See https://cloud.google.com/sdk/gcloud/reference/topic/configurations#:~:text=custom_ca_certs_file
				// Note: Env vars are the highest-precedence form of config.
				"CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE",
				// Used by nix.
				// See https://nix.dev/manual/nix/2.18/installation/env-variables#nix_ssl_cert_file
				"NIX_SSL_CERT_FILE",
			},
		})
		if err != nil {
			return nil, errors.Wrap(err, "expanding proxy build template")
		}
		uploads = append(uploads, upload{From: "/workspace/netlog.json", To: opts.RemoteMetadataStore.URL(Asset{Target: t, Type: ProxyNetlogAsset}).String()})
	} else {
		err := standardBuildTpl.Execute(&buildScript, map[string]any{
			"Dockerfile":        dockerfile,
			"UseSyscallMonitor": opts.UseSyscallMonitor,
			"SyscallPolicy":     tetragonPolicyJSON,
		})
		if err != nil {
			return nil, errors.Wrap(err, "expanding standard build template")
		}
	}
	var assetUploadScript bytes.Buffer
	err := assetUploadTpl.Execute(&assetUploadScript, map[string]any{
		"UtilPrebuildBucket": opts.UtilPrebuildBucket,
		"Uploads":            uploads,
	})
	if err != nil {
		return nil, errors.Wrap(err, "expanding asset upload template")
	}
	return &cloudbuild.Build{
		LogsBucket:     opts.LogsBucket,
		Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
		ServiceAccount: opts.BuildServiceAccount,
		Steps: []*cloudbuild.BuildStep{
			{
				Name:   "gcr.io/cloud-builders/docker",
				Script: buildScript.String(),
			},
			{
				Name: "gcr.io/cloud-builders/docker",
				Args: []string{"cp", "container:" + path.Join("/out", t.Artifact), path.Join("/workspace", t.Artifact)},
			},
			{
				Name:   "gcr.io/cloud-builders/docker",
				Script: "docker save img | gzip > /workspace/image.tgz",
			},
			{
				Name:   "docker.io/library/alpine:3.19",
				Script: assetUploadScript.String(),
			},
		},
	}, nil
}

func doCloudBuild(ctx context.Context, client gcb.Client, build *cloudbuild.Build, opts RemoteOptions, bi *BuildInfo) error {
	build, err := gcb.DoBuild(ctx, client, opts.Project, build)
	if err != nil {
		return errors.Wrap(err, "doing build")
	}
	bi.BuildEnd, err = time.Parse(time.RFC3339, build.FinishTime)
	if err != nil {
		return errors.Wrap(err, "extracting FinishTime")
	}
	bi.BuildID = build.Id
	bi.Steps = build.Steps
	bi.BuildImages = make(map[string]string)
	for i, s := range bi.Steps {
		bi.BuildImages[s.Name] = build.Results.BuildStepImages[i]
	}
	return gcb.ToError(build)
}

func makeDockerfile(input Input, opts RemoteOptions) (string, error) {
	env := BuildEnv{HasRepo: false, PreferPreciseToolchain: true}
	if opts.UseTimewarp {
		env.TimewarpHost = "localhost:8080"
	}
	instructions, err := input.Strategy.GenerateFor(input.Target, env)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate strategy")
	}
	dockerfile := new(bytes.Buffer)
	if input.Target.Ecosystem == Debian {
		err = debuildContainerTpl.Execute(dockerfile, rebuildContainerArgs{
			UseTimewarp:        opts.UseTimewarp,
			UtilPrebuildBucket: opts.UtilPrebuildBucket,
			Instructions:       instructions,
		})
	} else {
		err = alpineContainerTpl.Execute(dockerfile, rebuildContainerArgs{
			UseTimewarp:        opts.UseTimewarp,
			UtilPrebuildBucket: opts.UtilPrebuildBucket,
			Instructions:       instructions,
		})
	}
	if err != nil {
		return "", errors.Wrap(err, "populating template")
	}
	return dockerfile.String(), nil
}

// RebuildRemote executes the given target strategy on a remote builder.
func RebuildRemote(ctx context.Context, input Input, id string, opts RemoteOptions) error {
	t := input.Target
	bi := BuildInfo{Target: t, ID: id, Builder: os.Getenv("K_REVISION"), BuildStart: time.Now()}
	dockerfile, err := makeDockerfile(input, opts)
	if err != nil {
		return errors.Wrap(err, "creating dockerfile")
	}
	{
		lw, err := opts.LocalMetadataStore.Writer(ctx, Asset{Target: t, Type: DockerfileAsset})
		if err != nil {
			return errors.Wrap(err, "creating writer for Dockerfile")
		}
		defer lw.Close()
		rw, err := opts.DebugStore.Writer(ctx, Asset{Target: t, Type: DockerfileAsset})
		if err != nil {
			return errors.Wrap(err, "creating remote writer for Dockerfile")
		}
		defer rw.Close()
		if _, err := io.WriteString(io.MultiWriter(lw, rw), dockerfile); err != nil {
			return errors.Wrap(err, "writing Dockerfile")
		}
	}
	build, err := makeBuild(t, dockerfile, opts)
	if err != nil {
		return errors.Wrap(err, "creating build")
	}
	buildErr := errors.Wrap(doCloudBuild(ctx, opts.GCBClient, build, opts, &bi), "performing build")
	{
		lw, err := opts.LocalMetadataStore.Writer(ctx, Asset{Target: t, Type: BuildInfoAsset})
		if err != nil {
			return errors.Wrap(err, "creating writer for build info")
		}
		defer lw.Close()
		rw, err := opts.DebugStore.Writer(ctx, Asset{Target: t, Type: BuildInfoAsset})
		if err != nil {
			return errors.Wrap(err, "creating remote writer for build info")
		}
		defer rw.Close()
		if err := json.NewEncoder(io.MultiWriter(lw, rw)).Encode(bi); err != nil {
			return errors.Wrap(err, "marshalling and writing build info")
		}
	}
	return buildErr
}
