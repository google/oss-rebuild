// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import "cloud.google.com/go/vertexai/genai"

// Function is an implementation of an LLM tool.
type Function func(args map[string]any) genai.FunctionResponse

// FunctionDefinition is a function signature (FunctionDeclaration) along with its implementation (Function).
type FunctionDefinition struct {
	genai.FunctionDeclaration
	Function Function
}

// WithTools configures a copy of the provided model with the given function definitions.
func WithTools(baseModel genai.GenerativeModel, defs []*FunctionDefinition) *genai.GenerativeModel {
	declarations := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		if def != nil {
			declarations = append(declarations, &def.FunctionDeclaration)
		}
	}
	if len(declarations) > 0 {
		baseModel.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	} else {
		baseModel.Tools = nil
	}
	return &baseModel
}
