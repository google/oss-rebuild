// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package golang

import (
	"encoding/base64"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// GoBuild represents a build strategy for a Go module.
type GoBuild struct {
	rebuild.Location
}

var _ rebuild.Strategy = &GoBuild{}

func (b *GoBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "go/buildziptool",
			With: map[string]string{
				"mod":    mod,
				"script": script,
			},
		}},
		Build: []flow.Step{{
			Runs: `mkdir /out && cd /tools/ && go run ./main.go {{.Target.Package}} {{.Target.Version}} /src{{.Location.Dir}}`,
		}},
		OutputPath: "../out/module.zip",
	}
}

func (b *GoBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var (
	mod = base64.StdEncoding.EncodeToString([]byte(`
module gomodzip

go 1.25.4

require golang.org/x/mod v0.30.0
`))
	script = base64.StdEncoding.EncodeToString([]byte(`
package main

import (
	"os"
	"log"
	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

func main() {
	f, err := os.Create("/out/module.zip")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	m := module.Version{Path: os.Args[1], Version: os.Args[2]}
	if err := zip.CreateFromDir(f, m, os.Args[3]); err != nil {
		log.Fatal(err)
	}
}
`))
)

var toolkit = []*flow.Tool{
	{
		Name: "go/buildziptool",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
			mkdir /tools/
			echo {{.With.mod}} | base64 -d > /tools/go.mod
			echo {{.With.script}} | base64 -d > /tools/main.go
			cd /tools/
			go mod tidy
			go build ./main.go`)[1:],
		}},
	},
}
