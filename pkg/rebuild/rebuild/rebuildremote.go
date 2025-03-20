// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	UtilPrebuildDir     string
	UtilPrebuildAuth    bool
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
	UtilPrebuildDir    string
	UtilPrebuildAuth   bool
}

var tetragonPoliciesYaml = []string{`apiVersion: cilium.io/v1alpha1
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
`,
	`apiVersion: cilium.io/v1alpha1
kind: TracingPolicy
metadata:
  name: "file-open-at"
spec:
  tracepoints:
  - subsystem: syscalls
    event: sys_enter_openat
    args:
    - index: 5
      type: int32
    - index: 6
      type: string
    - index: 7
      type: uint32
    - index: 8
      type: uint32
`}

var tetragonPoliciesJSON []string

func init() {
	for _, policyYaml := range tetragonPoliciesYaml {
		var data any
		if err := yaml.Unmarshal([]byte(policyYaml), &data); err != nil {
			log.Fatalf("Malformed tetragon policy: %v", err)
		}
		b, err := json.Marshal(data)
		if err != nil {
			log.Fatalf("Converting tetragon policy to json: %v", err)
		}
		if bytes.Contains(b, []byte("'")) {
			log.Fatalf("Policy cannot contain single quotes: %s", string(b))
		}
		tetragonPoliciesJSON = append(tetragonPoliciesJSON, string(b))
	}
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
				#syntax=docker/dockerfile:1.10
				FROM docker.io/library/debian:trixie-20250203-slim
				RUN {{if .UtilPrebuildAuth}}--mount=type=secret,id=auth_header {{end}}<<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 curl {{if .UtilPrebuildAuth}}-H @/run/secrets/auth_header {{end -}}
				 https://{{.UtilPrebuildBucket}}.storage.googleapis.com/{{if .UtilPrebuildDir}}{{.UtilPrebuildDir}}/{{end}}timewarp > timewarp
				 chmod +x timewarp
				{{- end}}
				 apt update
				 apt install -y {{join " " .Instructions.SystemDeps}}
				EOF
				RUN <<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 ./timewarp -port 8080 &
				 while ! nc -z localhost 8080;do sleep 1;done
				{{- end}}
				 mkdir /src && cd /src
				 {{.Instructions.Source| indent}}
				 {{.Instructions.Deps | indent}}
				EOF
				RUN cat <<'EOF' >/build
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
				#syntax=docker/dockerfile:1.10
				FROM docker.io/library/alpine:3.19
				RUN {{if .UtilPrebuildAuth}}--mount=type=secret,id=auth_header {{end}}<<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 {{if .UtilPrebuildAuth}}apk add curl && curl -O -H @/run/secrets/auth_header {{else}}wget {{end -}}
				  https://{{.UtilPrebuildBucket}}.storage.googleapis.com/{{if .UtilPrebuildDir}}{{.UtilPrebuildDir}}/{{end}}timewarp
				 chmod +x timewarp
				{{- end}}
				 apk add {{join " " .Instructions.SystemDeps}}
				EOF
				RUN <<'EOF'
				 set -eux
				{{- if .UseTimewarp}}
				 ./timewarp -port 8080 &
				 while ! nc -z localhost 8080;do sleep 1;done
				{{- end}}
				 mkdir /src && cd /src
				 {{.Instructions.Source| indent}}
				 {{.Instructions.Deps | indent}}
				EOF
				RUN cat <<'EOF' >/build
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
				echo 'Starting rebuild for {{.TargetStr}}'
				{{- if .UseSyscallMonitor}}
				touch /workspace/tetragon.jsonl
				mkdir /workspace/tetragon/
				{{- range $i, $policy := .SyscallPolicies}}
				echo '{{$policy}}' > "/workspace/tetragon/policy_{{ $i }}.json"
				{{- end}}
				export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/workspace/tetragon/:/workspace/tetragon/ -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --tracing-policy-dir=/workspace/tetragon/ --export-filename=/workspace/tetragon.jsonl)
				grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
				{{- end}}
				{{- if .UtilPrebuildAuth}}
				apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@{{.Project}}.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
				(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
				{{- end}}
				cat <<'EOS' | docker buildx build {{if .UtilPrebuildAuth}}--secret id=auth_header,src=/tmp/auth_header {{end}}--tag=img -
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
				echo 'Starting rebuild for {{.TargetStr}}'
				{{- if .UtilPrebuildAuth}}
				apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@{{.Project}}.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
				(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
				{{- end}}
				curl -O {{if .UtilPrebuildAuth}}-H @/tmp/auth_header {{end -}}
					https://{{.UtilPrebuildBucket}}.storage.googleapis.com/{{if .UtilPrebuildDir}}{{.UtilPrebuildDir}}/{{end}}proxy
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
				{{- /* NOTE: File-based mounting does not appear to work here so use an env var. */ -}}
				{{- if .UtilPrebuildAuth}}
				(printf 'HEADER='; cat /tmp/auth_header) > /tmp/envfile
				{{- end}}
				docker run --detach --name=build --network=proxynet --entrypoint=/bin/sh {{if .UtilPrebuildAuth}}--env-file /tmp/envfile {{end}}gcr.io/cloud-builders/docker -c 'sleep infinity'
				docker exec --privileged build /bin/sh -euxc '
					iptables -t nat -A OUTPUT -p tcp --dport {{.HTTPPort}} -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp --dport {{.TLSPort}} -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp -m owner --uid-owner '$uid' -j ACCEPT
					iptables -t nat -A OUTPUT -p tcp --dport 80 -j DNAT --to-destination '$proxyIP':{{.HTTPPort}}
					iptables -t nat -A OUTPUT -p tcp --dport 443 -j DNAT --to-destination '$proxyIP':{{.TLSPort}}
				'
				{{- if .UseSyscallMonitor}}
				touch /workspace/tetragon.jsonl
				mkdir /workspace/tetragon/
				{{- range $i, $policy := .SyscallPolicies}}
				echo '{{$policy}}' > "/workspace/tetragon/policy_{{ $i }}.json"
				{{- end}}
				export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/workspace/tetragon/:/workspace/tetragon/ -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --tracing-policy-dir=/workspace/tetragon/ --export-filename=/workspace/tetragon.jsonl)
				grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
				{{- end}}
				docker exec build /bin/sh -euxc '
					curl http://proxy:{{.CtrlPort}}/cert | tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt
					export DOCKER_HOST=tcp://proxy:{{.DockerPort}} PROXYCERT=/etc/ssl/certs/proxy.crt{{if .UtilPrebuildAuth}} HEADER{{end}}
					docker buildx create --name proxied --bootstrap --driver docker-container --driver-opt network=container:build
					cat <<EOS | sed "s|^RUN|RUN --mount=type=bind,from=certs,dst=/etc/ssl/certs{{range .CertEnvVars}} --mount=type=secret,id=PROXYCERT,env={{.}}{{end}}|" | \
						docker buildx build --builder proxied --build-context certs=/etc/ssl/certs --secret id=PROXYCERT {{if .UtilPrebuildAuth}}--secret id=auth_header,env=HEADER {{end}}--load --tag=img -
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
				{{- if .UtilPrebuildAuth}}
				apk add curl jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/builder-remote@{{.Project}}.iam.gserviceaccount.com/token | jq .access_token > /tmp/token
				(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
				{{- end}}
				{{if .UtilPrebuildAuth}}curl -O -H @/tmp/auth_header {{else}}wget {{end -}}
				 https://{{.UtilPrebuildBucket}}.storage.googleapis.com/{{if .UtilPrebuildDir}}{{.UtilPrebuildDir}}/{{end}}gsutil_writeonly
				chmod +x gsutil_writeonly
				{{- range .Uploads}}
				./gsutil_writeonly cp {{.From}} {{.To}}
				{{- end}}
				`)[1:], // remove leading newline
	))

func makeBuild(t Target, dockerfile string, opts RemoteOptions) (*cloudbuild.Build, error) {
	var buildScript bytes.Buffer
	uploads := []upload{
		{From: "/workspace/image.tgz", To: opts.RemoteMetadataStore.URL(ContainerImageAsset.For(t)).String()},
		{From: path.Join("/workspace", t.Artifact), To: opts.RemoteMetadataStore.URL(RebuildAsset.For(t)).String()},
	}
	if opts.UseSyscallMonitor {
		uploads = append(uploads, upload{From: "/workspace/tetragon.jsonl", To: opts.RemoteMetadataStore.URL(TetragonLogAsset.For(t)).String()})
	}
	if opts.UseNetworkProxy {
		err := proxyBuildTpl.Execute(&buildScript, map[string]any{
			"TargetStr":          fmt.Sprintf("%+v", t),
			"UtilPrebuildBucket": opts.UtilPrebuildBucket,
			"UtilPrebuildDir":    opts.UtilPrebuildDir,
			"UtilPrebuildAuth":   opts.UtilPrebuildAuth,
			"Project":            opts.Project,
			"Dockerfile":         dockerfile,
			"UseSyscallMonitor":  opts.UseSyscallMonitor,
			"SyscallPolicies":    tetragonPoliciesJSON,
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
		uploads = append(uploads, upload{From: "/workspace/netlog.json", To: opts.RemoteMetadataStore.URL(ProxyNetlogAsset.For(t)).String()})
	} else {
		err := standardBuildTpl.Execute(&buildScript, map[string]any{
			"TargetStr":         fmt.Sprintf("%+v", t),
			"Dockerfile":        dockerfile,
			"UseSyscallMonitor": opts.UseSyscallMonitor,
			"SyscallPolicies":   tetragonPoliciesJSON,
			"UtilPrebuildAuth":  opts.UtilPrebuildAuth,
			"Project":           opts.Project,
		})
		if err != nil {
			return nil, errors.Wrap(err, "expanding standard build template")
		}
	}
	var assetUploadScript bytes.Buffer
	err := assetUploadTpl.Execute(&assetUploadScript, map[string]any{
		"UtilPrebuildBucket": opts.UtilPrebuildBucket,
		"UtilPrebuildDir":    opts.UtilPrebuildDir,
		"UtilPrebuildAuth":   opts.UtilPrebuildAuth,
		"Project":            opts.Project,
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
	var deadline time.Time
	if d, ok := ctx.Value(GCBDeadlineID).(time.Time); ok {
		deadline = d
	}
	buildCtx := ctx
	if !deadline.IsZero() {
		// TODO: We will need to use build.Timeout for builds over 60 minutes, because this calling code cannot wait around, and also to extend the GCB default.
		bctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		buildCtx = bctx
	}
	build, err := gcb.DoBuild(buildCtx, client, opts.Project, build)
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
	buildErr := gcb.ToError(build)
	// Don't try to read BuildStepImages if the build failed.
	// It's possible we're missing some valid BuildStepImages this way, but not super important.
	if buildErr == nil {
		for i, s := range bi.Steps {
			bi.BuildImages[s.Name] = build.Results.BuildStepImages[i]
		}
	}
	return buildErr
}

func MakeDockerfile(input Input, opts RemoteOptions) (string, error) {
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
			UtilPrebuildDir:    opts.UtilPrebuildDir,
			UtilPrebuildAuth:   opts.UtilPrebuildAuth,
			Instructions:       instructions,
		})
	} else {
		err = alpineContainerTpl.Execute(dockerfile, rebuildContainerArgs{
			UseTimewarp:        opts.UseTimewarp,
			UtilPrebuildBucket: opts.UtilPrebuildBucket,
			UtilPrebuildDir:    opts.UtilPrebuildDir,
			UtilPrebuildAuth:   opts.UtilPrebuildAuth,
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
	dockerfile, err := MakeDockerfile(input, opts)
	if err != nil {
		return errors.Wrap(err, "creating dockerfile")
	}
	{
		lw, err := opts.LocalMetadataStore.Writer(ctx, DockerfileAsset.For(t))
		if err != nil {
			return errors.Wrap(err, "creating writer for Dockerfile")
		}
		defer lw.Close()
		rw, err := opts.DebugStore.Writer(ctx, DockerfileAsset.For(t))
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
	// TODO: Maybe we should copy the GCB logs to the debug bucket to make them more accessible?
	{
		lw, err := opts.LocalMetadataStore.Writer(ctx, BuildInfoAsset.For(t))
		if err != nil {
			return errors.Wrap(err, "creating writer for build info")
		}
		defer lw.Close()
		rw, err := opts.DebugStore.Writer(ctx, BuildInfoAsset.For(t))
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
