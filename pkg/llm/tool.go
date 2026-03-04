// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import "google.golang.org/genai"

// Function is an implementation of an LLM tool.
type Function func(args map[string]any) genai.FunctionResponse

// FunctionDefinition is a function signature (FunctionDeclaration) along with its implementation (Function).
type FunctionDefinition struct {
	genai.FunctionDeclaration
	Function Function
}

// WithTools configures the provided config with the given function definitions.
func WithTools(config *genai.GenerateContentConfig, defs []*FunctionDefinition) *genai.GenerateContentConfig {
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}
	declarations := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		if def != nil {
			declarations = append(declarations, &def.FunctionDeclaration)
		}
	}
	if len(declarations) > 0 {
		config.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	} else {
		config.Tools = nil
	}
	return config
}
