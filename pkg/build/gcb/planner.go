// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"gopkg.in/yaml.v3"
)

// upload struct for asset upload template
type upload struct {
	From string
	To   string
}

// tetragonPoliciesYaml contains the Tetragon policy used for build syscall monitoring
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
`,
}

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

type gcbContainerArgs struct {
	rebuild.Instructions
	BaseImage       string
	OS              build.OS
	PackageManager  build.PackageManagerCommands
	UseTimewarp     bool
	UseNetworkProxy bool
	TimewarpURL     string
	ProxyURL        string
	TimewarpAuth    bool
	ProxyAuth       bool
}

// gcbDockerfileTpl generates Dockerfiles for use in GCB
var gcbDockerfileTpl = template.Must(
	template.New("gcb dockerfile").Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n\t") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
		"list":   func(s string) []string { return []string{s} },
	}).Parse(
		textwrap.Dedent(`
			#syntax=docker/dockerfile:1.10
			FROM {{.BaseImage}}
			RUN {{if or .TimewarpAuth .ProxyAuth}}--mount=type=secret,id=auth_header {{end}}<<-'EOF'
				set -eux
			{{- if .UseTimewarp}}
				{{- $hasCurl := or (eq .OS "debian") (eq .OS "ubuntu")}}
				{{- $hasWget := eq .OS "alpine"}}
				{{- if .TimewarpAuth}}
				{{if not $hasCurl}}{{.PackageManager.InstallCommand (list "curl")}} && {{end}}curl -O -H @/run/secrets/auth_header {{.TimewarpURL}}
				{{- else if $hasWget}}
				wget {{.TimewarpURL}}
				{{- else if $hasCurl}}
				curl -O {{.TimewarpURL}}
				{{- end}}
				chmod +x timewarp
			{{- end}}
				{{- if eq .OS "debian"}}
				{{.PackageManager.UpdateCmd}}
				{{- end}}
				{{.PackageManager.InstallCommand .Instructions.Requires.SystemDeps}}
				EOF
			RUN <<-'EOF'
				set -eux
			{{- if .UseTimewarp}}
				./timewarp -port 8080 &
				while ! nc -z localhost 8080;do sleep 1;done
			{{- end}}
				mkdir /src && cd /src
				{{.Instructions.Source| indent}}
				{{.Instructions.Deps | indent}}
				EOF
			COPY --chmod=755 <<-'EOF' /build
				set -eux
				{{.Instructions.Build | indent}}
				mkdir /out && cp /src/{{.Instructions.OutputPath}} /out/
				EOF
			WORKDIR "/src"
			ENTRYPOINT ["/bin/sh","/build"]
			`)[1:], // remove leading newline
	))

// gcbStandardBuildTpl generates standard build scripts for Cloud Build steps
var gcbStandardBuildTpl = template.Must(
	template.New("gcb standard build script").Parse(
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
			{{- if .TimewarpAuth}}
			apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/{{.ServiceAccountEmail}}/token | jq .access_token > /tmp/token
			(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
			{{- end}}
			cat <<'EOS' | docker buildx build {{if .TimewarpAuth}}--secret id=auth_header,src=/tmp/auth_header {{end}}--tag=img -
			{{.Dockerfile}}
			EOS
			docker run {{if .Privileged}}--privileged {{end}}--name=container img
			{{- if .UseSyscallMonitor}}
			docker kill tetragon
			{{- end}}
			`)[1:], // remove leading newline
	))

// gcbProxyBuildTpl generates proxy-enabled build scripts for Cloud Build steps
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
var gcbProxyBuildTpl = template.Must(
	template.New("gcb proxy build script").Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n\t") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		textwrap.Dedent(`
			set -eux
			echo 'Starting rebuild for {{.TargetStr}}'
			{{- if .ProxyAuth}}
			apt install -y jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/{{.ServiceAccountEmail}}/token | jq .access_token > /tmp/token
			(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
			{{- end}}
			curl -O {{if .ProxyAuth}}-H @/tmp/auth_header {{end -}} {{.ProxyURL}}
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
			{{- if .TimewarpAuth}}
			(printf 'HEADER='; cat /tmp/auth_header) > /tmp/envfile
			{{- end}}
			docker run --detach --name=build --network=proxynet --entrypoint=/bin/sh {{if .TimewarpAuth}}--env-file /tmp/envfile {{end}}gcr.io/cloud-builders/docker -c 'sleep infinity'
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
			cat <<-'EOS' | sed "s|^RUN|RUN --mount=type=bind,from=certs,dst=/etc/ssl/certs{{range .CertEnvVars}} --mount=type=secret,id=PROXYCERT,env={{.}}{{end}}|" > /Dockerfile
				{{.Dockerfile | indent}}
				EOS
			docker cp /Dockerfile build:/Dockerfile
			docker exec build /bin/sh -euxc '
				curl http://proxy:{{.CtrlPort}}/cert | tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt
				export DOCKER_HOST=tcp://proxy:{{.DockerPort}} PROXYCERT=/etc/ssl/certs/proxy.crt{{if .TimewarpAuth}} HEADER{{end}}
				docker buildx create --name proxied --bootstrap --driver docker-container --driver-opt network=container:build
				cat /Dockerfile | docker buildx build --builder proxied --build-context certs=/etc/ssl/certs --secret id=PROXYCERT {{if .TimewarpAuth}}--secret id=auth_header,env=HEADER {{end}}--load --tag=img -
				docker run {{if .Privileged}}--privileged {{end}}--name=container img
			'
			{{- if .UseSyscallMonitor}}
			docker kill tetragon
			{{- end}}
			curl http://proxy:{{.CtrlPort}}/summary > /workspace/netlog.json
			`)[1:], // remove leading newline
	))

// Planner generates Google Cloud Build execution plans
type Planner struct {
	// GCB-specific configuration
	project        string
	serviceAccount string
	logsBucket     string
	privatePool    *gcb.PrivatePoolConfig

	// Internal configuration - not exposed to users
	timewarpHost    string
	hasRepo         bool
	allowPrivileged bool
}

// NewPlanner creates a new GCB planner with the given configuration
func NewPlanner(config PlannerConfig) *Planner {
	return &Planner{
		project:         config.Project,
		serviceAccount:  config.ServiceAccount,
		logsBucket:      config.LogsBucket,
		privatePool:     config.PrivatePool,
		timewarpHost:    "localhost:8080", // Internal default
		hasRepo:         false,            // Repository needs to be cloned in container
		allowPrivileged: config.AllowPrivileged,
	}
}

// PlannerConfig contains configuration for the GCB planner
type PlannerConfig struct {
	Project         string
	ServiceAccount  string
	LogsBucket      string
	PrivatePool     *gcb.PrivatePoolConfig
	AllowPrivileged bool
}

// GeneratePlan implements Planner[*Plan]
func (p *Planner) GeneratePlan(ctx context.Context, input rebuild.Input, opts build.PlanOptions) (*Plan, error) {
	// Generate rebuild instructions from the strategy
	buildEnv := rebuild.BuildEnv{
		TimewarpHost: p.timewarpHost,
		HasRepo:      p.hasRepo,
	}
	instructions, err := input.Strategy.GenerateFor(input.Target, buildEnv)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate rebuild instructions")
	}
	// Generate Dockerfile
	dockerfile, err := p.generateDockerfile(instructions, input, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate Dockerfile")
	}
	// Generate Cloud Build steps
	steps, err := p.generateSteps(input.Target, dockerfile, instructions.Requires, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate Cloud Build steps")
	}
	return &Plan{
		Steps:      steps,
		Dockerfile: dockerfile,
	}, nil
}

// generateDockerfile creates a Dockerfile from rebuild instructions and options using templates
func (p *Planner) generateDockerfile(instructions rebuild.Instructions, input rebuild.Input, opts build.PlanOptions) (string, error) {
	// Select base image using the configured selection logic
	baseImage := opts.Resources.BaseImageConfig.SelectFor(input)
	// Detect OS and get package manager commands
	os := build.DetectOS(baseImage)
	pkgMgr := build.GetPackageManagerCommands(os)
	// Convert tool URLs and determine auth requirements
	timewarpURL, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process timewarp URL")
	}
	proxyURL, proxyAuth, err := p.getToolURL(build.ProxyTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process proxy URL")
	}
	// Create template args using converted URLs and prebuild config
	args := gcbContainerArgs{
		Instructions:    instructions,
		BaseImage:       baseImage,
		OS:              os,
		PackageManager:  pkgMgr,
		UseTimewarp:     opts.UseTimewarp,
		UseNetworkProxy: opts.UseNetworkProxy,
		TimewarpURL:     timewarpURL,
		ProxyURL:        proxyURL,
		TimewarpAuth:    timewarpAuth,
		ProxyAuth:       proxyAuth,
	}
	var buf bytes.Buffer
	if err := gcbDockerfileTpl.Execute(&buf, args); err != nil {
		return "", errors.Wrap(err, "failed to execute Dockerfile template")
	}
	return buf.String(), nil
}

// getToolURL extracts the URL and auth requirements for a given tool
func (p *Planner) getToolURL(toolType build.ToolType, opts build.PlanOptions) (toolURL string, needsAuth bool, err error) {
	originalURL, exists := opts.Resources.ToolURLs[toolType]
	if !exists {
		return "", false, nil
	}
	convertedURL, err := build.ConvertURLForRuntime(originalURL)
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to convert URL for %s", toolType)
	}
	needsAuth = build.NeedsAuth(originalURL, opts.Resources.ToolAuthRequired)
	return convertedURL, needsAuth, nil
}

// generateSteps creates the Cloud Build steps using remote rebuild patterns
func (p *Planner) generateSteps(target rebuild.Target, dockerfile string, reqs rebuild.RequiredEnv, opts build.PlanOptions) ([]*cloudbuild.BuildStep, error) {
	var steps []*cloudbuild.BuildStep
	// Main build step - either standard or proxy-enabled
	buildScript, err := p.generateRemoteBuildScript(target, dockerfile, reqs, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate remote build script")
	}
	buildStep := &cloudbuild.BuildStep{
		Name:   "gcr.io/cloud-builders/docker",
		Script: buildScript,
	}
	steps = append(steps, buildStep)
	// Extract artifacts from container
	extractStep := &cloudbuild.BuildStep{
		Name: "gcr.io/cloud-builders/docker",
		Args: []string{"cp", "container:" + path.Join("/out", target.Artifact), path.Join("/workspace", target.Artifact)},
	}
	steps = append(steps, extractStep)
	// Save container image
	saveStep := &cloudbuild.BuildStep{
		Name:   "gcr.io/cloud-builders/docker",
		Script: "docker save img | gzip > /workspace/image.tgz",
	}
	steps = append(steps, saveStep)
	// Upload assets
	uploadScript, err := p.generateAssetUploadScript(target, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate asset upload script")
	}
	uploadStep := &cloudbuild.BuildStep{
		Name:   opts.Resources.BaseImageConfig.Default,
		Script: uploadScript,
	}
	steps = append(steps, uploadStep)
	return steps, nil
}

// generateRemoteBuildScript creates the build script for remote rebuild mode
func (p *Planner) generateRemoteBuildScript(target rebuild.Target, dockerfile string, reqs rebuild.RequiredEnv, opts build.PlanOptions) (string, error) {
	if opts.UseNetworkProxy {
		return p.generateProxyBuildScript(target, dockerfile, reqs, opts)
	}
	return p.generateStandardBuildScript(target, dockerfile, reqs, opts)
}

// generateStandardBuildScript creates a standard build script without proxy
func (p *Planner) generateStandardBuildScript(target rebuild.Target, dockerfile string, reqs rebuild.RequiredEnv, opts build.PlanOptions) (string, error) {
	_, serviceAccountEmail := path.Split(p.serviceAccount)
	_, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process timewarp URL")
	}
	var privileged bool
	if reqs.Privileged {
		if p.allowPrivileged {
			privileged = true
		} else {
			log.Println("Warning: instructions requested privileged execution but this planner does not allow privileged builds.")
		}
	}
	var buf bytes.Buffer
	if err := gcbStandardBuildTpl.Execute(&buf, map[string]any{
		"TargetStr":           fmt.Sprintf("%+v", target),
		"Dockerfile":          dockerfile,
		"Privileged":          privileged,
		"UseSyscallMonitor":   opts.UseSyscallMonitor,
		"SyscallPolicies":     tetragonPoliciesJSON,
		"TimewarpAuth":        timewarpAuth,
		"ServiceAccountEmail": serviceAccountEmail,
	}); err != nil {
		return "", errors.Wrap(err, "failed to execute standard build template")
	}
	return buf.String(), nil
}

// generateProxyBuildScript creates a proxy-enabled build script
func (p *Planner) generateProxyBuildScript(target rebuild.Target, dockerfile string, reqs rebuild.RequiredEnv, opts build.PlanOptions) (string, error) {
	_, serviceAccountEmail := path.Split(p.serviceAccount)
	proxyURL, proxyAuth, err := p.getToolURL(build.ProxyTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process proxy URL")
	}
	_, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process timewarp URL")
	}
	var privileged bool
	if reqs.Privileged {
		if p.allowPrivileged {
			privileged = true
		} else {
			log.Println("Warning: instructions requested privileged execution but this planner does not allow privileged builds.")
		}
	}
	var buf bytes.Buffer
	if err := gcbProxyBuildTpl.Execute(&buf, map[string]any{
		"TargetStr":           fmt.Sprintf("%+v", target),
		"Dockerfile":          dockerfile,
		"Privileged":          privileged,
		"UseSyscallMonitor":   opts.UseSyscallMonitor,
		"SyscallPolicies":     tetragonPoliciesJSON,
		"TimewarpAuth":        timewarpAuth,
		"ProxyURL":            proxyURL,
		"ProxyAuth":           proxyAuth,
		"ServiceAccountEmail": serviceAccountEmail,
		"User":                "proxyu",
		"HTTPPort":            "3128",
		"TLSPort":             "3129",
		"CtrlPort":            "3127",
		"DockerPort":          "3130",
		"CertEnvVars":         []string{"PIP_CERT", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE", "NIX_SSL_CERT_FILE"},
	}); err != nil {
		return "", errors.Wrap(err, "failed to execute proxy build template")
	}
	return buf.String(), nil
}

// generateAssetUploadScript creates the script for uploading assets
func (p *Planner) generateAssetUploadScript(target rebuild.Target, opts build.PlanOptions) (string, error) {
	var uploads []upload
	// Add uploads for each asset if asset store is configured
	if opts.Resources.AssetStore != nil {
		assetTypes := []rebuild.AssetType{
			rebuild.ContainerImageAsset,
			rebuild.RebuildAsset,
		}
		if opts.UseSyscallMonitor {
			assetTypes = append(assetTypes, rebuild.TetragonLogAsset)
		}
		if opts.UseNetworkProxy {
			assetTypes = append(assetTypes, rebuild.ProxyNetlogAsset)
		}
		for _, assetType := range assetTypes {
			url := opts.Resources.AssetStore.URL(assetType.For(target))
			if url == nil {
				return "", errors.Errorf("no valid upload path for %s", assetType)
			}
			switch assetType {
			case rebuild.ContainerImageAsset:
				uploads = append(uploads, upload{
					From: "/workspace/image.tgz",
					To:   url.String(),
				})
			case rebuild.RebuildAsset:
				uploads = append(uploads, upload{
					From: path.Join("/workspace", target.Artifact),
					To:   url.String(),
				})
			case rebuild.TetragonLogAsset:
				uploads = append(uploads, upload{
					From: "/workspace/tetragon.jsonl",
					To:   url.String(),
				})
			case rebuild.ProxyNetlogAsset:
				uploads = append(uploads, upload{
					From: "/workspace/netlog.json",
					To:   url.String(),
				})
			}
		}
	}
	_, serviceAccountEmail := path.Split(p.serviceAccount)
	var buf bytes.Buffer
	gsutilURL, gsutilAuth, err := p.getToolURL(build.GSUtilTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to process timewarp URL")
	}
	if err := gcbAssetUploadTpl.Execute(&buf, map[string]any{
		"GSUtilURL":           gsutilURL,
		"GSUtilAuth":          gsutilAuth,
		"ServiceAccountEmail": serviceAccountEmail,
		"Uploads":             uploads,
	}); err != nil {
		return "", errors.Wrap(err, "failed to execute asset upload template")
	}
	return buf.String(), nil
}

// gcbAssetUploadTpl for asset upload script
var gcbAssetUploadTpl = template.Must(
	template.New("gcb asset upload").Parse(
		textwrap.Dedent(`
			set -eux
			{{- if .GSUtilAuth}}
			apk add curl jq && curl -H Metadata-Flavor:Google http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/{{.ServiceAccountEmail}}/token | jq .access_token > /tmp/token
			(printf "Authorization: Bearer "; cat /tmp/token) > /tmp/auth_header
			curl -O -H @/tmp/auth_header {{.GSUtilURL}}
			{{- else}}
			wget {{.GSUtilURL}}
			{{- end}}
			chmod +x gsutil_writeonly
			{{- range .Uploads}}
			./gsutil_writeonly cp {{.From}} {{.To}}
			{{- end}}
			`)[1:], // remove leading newline
	))
