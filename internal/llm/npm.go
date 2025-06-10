// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/textwrap"
	"google.golang.org/genai"
)

var NPMSystemPrompt = genai.NewPartFromText("You are an expert Javascript and Typescript developer who is helpful, insightful, and thoughtful.")

type originalParams struct {
	PackageJSON string
}

var originalPromptTpl = template.Must(
	template.New(
		"infer original build",
	).Parse(
		textwrap.Dedent(`
				Goal: Guess the bash command(s) that were used to publish the npm package.
				This often uses user-defined scripts in the package.json.
				Assume you start from a clean state.

				{{- if .PackageJSON}}package.json:
				` + "```" + `
				{{.PackageJSON}}
				` + "```" + `
				{{- end}}
				`)[1:],
	))

func executeTemplate(tpl *template.Template, data any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := tpl.Execute(buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type inferParams struct {
	Original    ScriptResponse
	PackageJSON string
}

var inferPromptTpl = template.Must(
	template.New(
		"infer rebuild",
	).Funcs(template.FuncMap{
		"join": func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		textwrap.Dedent(`
				Goal: Construct the bash commands to create the npm archive as it was originally published.
				After the commands are run, the archive must be present in the filesystem so "npm pack" should generally be run last.
				This often involves user-defined scripts in the package.json to build the code and archive.
				Try to avoid running tests, lints, or other unnecessary commands.
				Prefer installing all dev dependencies using "npm install --force" if necessary instead of individual deps.
				Only npm is installed by default so additional deps like yarn need to be installed separately.
				Prefer using "npm run <user-defined-script>" if one satisfies some or all of the goal.
				If there is a release or publish script, you can customize it to avoid publishing.
				If a script nearly satisfies the goal, feel free to repurpose parts of those scripts to achieve the goal.
				Assume you start from a clean state.

				Another user predicted the original publication process used the following script because {{.Original.Reason}}:

				{{.Original.Commands | join "\n"}}

				{{- if .PackageJSON}}package.json:
				` + "```" + `
				{{.PackageJSON}}
				` + "```" + `
				{{- end}}
				`)[1:],
	))

// InferNPMBuild attempts to generate an NPM package build script.
func InferNPMBuild(ctx context.Context, client *genai.Client, model string, packageJSON string) (*ScriptResponse, error) {
	var resp ScriptResponse
	config := &genai.GenerateContentConfig{
		ResponseSchema:   ScriptResponseSchema,
		ResponseMIMEType: JSONMIMEType,
		Temperature:      genai.Ptr[float32](1.),
	}
	config = WithSystemPrompt(config, NPMSystemPrompt)
	originalReleasePrompt, err := executeTemplate(originalPromptTpl, originalParams{
		PackageJSON: packageJSON,
	})
	if err != nil {
		return nil, err
	}
	if err := GenerateTypedContent(ctx, client, model, config, &resp, genai.NewPartFromText(originalReleasePrompt)); err != nil {
		return nil, err
	}
	inferPrompt, err := executeTemplate(inferPromptTpl, inferParams{
		Original:    resp,
		PackageJSON: packageJSON,
	})
	if err != nil {
		return nil, err
	}
	if err := GenerateTypedContent(ctx, client, model, config, &resp, genai.NewPartFromText(inferPrompt)); err != nil {
		return nil, err
	}
	return &resp, nil
}

type recoverParams struct {
	Script      string
	PackageJSON string
	BuildLog    string
}

var recoverPromptTpl = template.Must(
	template.New(
		"recover build failure",
	).Parse(
		textwrap.Dedent(`
				Goal: Fix the provided build script to address the error.
				After the commands are run, the archive must be present in the filesystem so "npm pack" should generally be run last.
				Prefer installing all dev dependencies using "npm install --force" if necessary instead of individual deps.
				Only npm is installed by default so additional deps like yarn need to be installed separately.
				{{- if .Script}}build_script.sh:
				` + "```" + `
				{{.Script}}
				` + "```" + `
				{{- end}}
				{{- if .PackageJSON}}package.json:
				` + "```" + `
				{{.PackageJSON}}
				` + "```" + `
				{{- end}}
				{{- if .BuildLog}}build.log:
				` + "```" + `
				{{.BuildLog}}
				` + "```" + `
				{{- end}}
				`)[1:],
	))

// FixNPMBreakage attempts to repair an observed build failure by suggesting another script.
func FixNPMBreakage(ctx context.Context, client *genai.Client, model string, script, packageJSON, log string) (*ScriptResponse, error) {
	p, err := executeTemplate(recoverPromptTpl, recoverParams{
		Script:      script,
		PackageJSON: packageJSON,
		BuildLog:    log,
	})
	if err != nil {
		return nil, err
	}
	var resp ScriptResponse
	config := &genai.GenerateContentConfig{
		ResponseSchema:   ScriptResponseSchema,
		ResponseMIMEType: JSONMIMEType,
		Temperature:      genai.Ptr[float32](1.),
	}
	config = WithSystemPrompt(config, NPMSystemPrompt)
	if err := GenerateTypedContent(ctx, client, model, config, &resp, genai.NewPartFromText(p)); err != nil {
		return nil, err
	}
	return &resp, nil
}
