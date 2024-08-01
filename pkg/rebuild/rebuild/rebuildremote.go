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
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"text/template"
	"time"

	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
)

// RemoteOptions provides the configuration to execute rebuilds on Cloud Build.
type RemoteOptions struct {
	GCBClient           gcb.Client
	Project             string
	BuildServiceAccount string
	LogsBucket          string
	// LocalMetadataStore stores the dockerfile and build info. Cloud build does not need access to this.
	LocalMetadataStore AssetStore
	// RemoteMetadataStore stores the rebuilt artifact. Cloud build needs access to upload artifacts here.
	RemoteMetadataStore AssetStore
	UtilPrebuildBucket  string
	// TODO: Consider moving this to Strategy.
	UseTimewarp bool
}

type rebuildContainerArgs struct {
	Instructions
	UseTimewarp        bool
	UtilPrebuildBucket string
}

var rebuildContainerTpl = template.Must(
	template.New(
		"rebuild container",
	).Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n ") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		// NOTE: For syntax docs, see https://docs.docker.com/build/dockerfile/release-notes/
		`#syntax=docker/dockerfile:1.4
{{- if .UseTimewarp}}
FROM gcr.io/cloud-builders/gsutil AS timewarp_provider
RUN gsutil cp -P gs://{{.UtilPrebuildBucket}}/timewarp .
{{- end}}
FROM alpine:3.19
{{- if .UseTimewarp}}
COPY --from=timewarp_provider ./timewarp .
{{- end}}
RUN <<'EOF'
 set -eux
{{- if .UseTimewarp}}
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
{{- end}}
 apk add {{join " " .Instructions.SystemDeps}}
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
`))

func makeBuild(t Target, dockerfile, imageUploadPath, rebuildUploadPath string, opts RemoteOptions) *cloudbuild.Build {
	return &cloudbuild.Build{
		LogsBucket:     opts.LogsBucket,
		Options:        &cloudbuild.BuildOptions{Logging: "GCS_ONLY"},
		ServiceAccount: opts.BuildServiceAccount,
		Steps: []*cloudbuild.BuildStep{
			{
				Name:   "gcr.io/cloud-builders/docker",
				Script: "cat <<'EOS' | docker buildx build --tag=img -\n" + dockerfile + "\nEOS",
			},
			{
				Name: "gcr.io/cloud-builders/docker",
				Args: []string{"run", "--name=container", "img"},
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
				Name: "gcr.io/cloud-builders/gsutil",
				Script: fmt.Sprintf(
					"gsutil cp -P gs://%s/gsutil_writeonly . && ./gsutil_writeonly %s && ./gsutil_writeonly %s",
					opts.UtilPrebuildBucket,
					strings.Join([]string{
						"cp",
						"/workspace/image.tgz",
						imageUploadPath,
					}, " "),
					strings.Join([]string{
						"cp",
						path.Join("/workspace", t.Artifact),
						rebuildUploadPath,
					}, " "),
				),
			},
		},
	}
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
	return nil
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
	err = rebuildContainerTpl.Execute(dockerfile, rebuildContainerArgs{
		UseTimewarp:        opts.UseTimewarp,
		UtilPrebuildBucket: opts.UtilPrebuildBucket,
		Instructions:       instructions,
	})
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
		w, _, err := opts.LocalMetadataStore.Writer(ctx, Asset{Target: t, Type: DockerfileAsset})
		if err != nil {
			return errors.Wrap(err, "creating writer for Dockerfile")
		}
		defer w.Close()
		if _, err := io.WriteString(w, dockerfile); err != nil {
			return errors.Wrap(err, "writing Dockerfile")
		}
	}
	// NOTE: Ignore the local writer since GCS doesn't flush writes until Close.
	// TODO: Could be resolved by adding ResourcePath() method.
	_, imageUploadPath, err := opts.RemoteMetadataStore.Writer(ctx, Asset{Target: t, Type: ContainerImageAsset})
	if err != nil {
		return errors.Wrap(err, "creating dummy writer for container image")
	}
	_, rebuildUploadPath, err := opts.RemoteMetadataStore.Writer(ctx, Asset{Target: t, Type: RebuildAsset})
	if err != nil {
		return errors.Wrap(err, "creating dummy writer for rebuild")
	}
	build := makeBuild(t, dockerfile, imageUploadPath, rebuildUploadPath, opts)
	if err := doCloudBuild(ctx, opts.GCBClient, build, opts, &bi); err != nil {
		return errors.Wrap(err, "performing build")
	}
	{
		w, _, err := opts.LocalMetadataStore.Writer(ctx, Asset{Target: t, Type: BuildInfoAsset})
		if err != nil {
			return errors.Wrap(err, "creating writer for build info")
		}
		defer w.Close()
		if err := json.NewEncoder(w).Encode(bi); err != nil {
			return errors.Wrap(err, "marshalling and writing build info")
		}
	}
	return nil
}
