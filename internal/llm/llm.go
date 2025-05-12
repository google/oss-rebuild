// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"encoding/json"

	"cloud.google.com/go/vertexai/genai"
	"github.com/pkg/errors"
)

var (
	// Model names supported by VertexAI.

	GeminiPro   = "gemini-2.5-pro-preview-03-25"
	GeminiFlash = "gemini-2.5-flash-preview-04-17"

	// Roles used for demarcating speakers.

	ModelRole = "model"
	UserRole  = "user"

	// MIME Types supported by VertexAI.

	JSONMIMEType = "application/json"
	TextMIMEType = "text/plain"
)

func WithSystemPrompt(model genai.GenerativeModel, prompt ...genai.Part) *genai.GenerativeModel {
	model.SystemInstruction = &genai.Content{
		Role:  ModelRole,
		Parts: prompt,
	}
	return &model
}

var ScriptResponseSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"reason": {
			Type:        genai.TypeString,
			Description: "The rationale and justification for provided commands",
		},
		"commands": {
			Type:        genai.TypeArray,
			Items:       &genai.Schema{Type: genai.TypeString, Description: "A shell command"},
			Description: "The shell commands that accomplish the requested task",
		},
	},
	Required: []string{"reason", "commands"},
}

type ScriptResponse struct {
	Reason   string
	Commands []string
}

func GenerateTextContent(ctx context.Context, model *genai.GenerativeModel, prompt ...genai.Part) (string, error) {
	resp, err := model.GenerateContent(ctx, prompt...)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate content")
	}
	if len(resp.Candidates) == 0 {
		return "", errors.New("no candidates returned")
	}
	candidate := resp.Candidates[0]
	if candidate.FinishReason != genai.FinishReasonStop {
		return "", errors.Errorf("generating content: %s", candidate.FinishMessage)
	}
	switch len(candidate.Content.Parts) {
	case 0:
		return "", errors.New("empty response content")
	case 1:
		return string(candidate.Content.Parts[0].(genai.Text)), nil
	default:
		return "", errors.New("multiple response parts")
	}
}

// GenerateTypedContent extracts JSON data from text according to the provided schema.
func GenerateTypedContent(ctx context.Context, model *genai.GenerativeModel, out any, prompt ...genai.Part) error {
	if model.GenerationConfig.ResponseSchema == nil {
		return errors.New("generate config must set a schema")
	}
	if model.GenerationConfig.ResponseMIMEType != JSONMIMEType {
		return errors.New("generate config must set a JSON MIME type")
	}
	text, err := GenerateTextContent(ctx, model, prompt...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return errors.Wrap(err, "parsing JSON response")
	}
	return nil
}
