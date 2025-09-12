// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	"google.golang.org/genai"
)

var (
	// Model names supported by VertexAI.

	GeminiPro   = "gemini-2.5-pro"
	GeminiFlash = "gemini-2.5-flash"

	// Roles used for demarcating speakers.

	ModelRole = "model"
	UserRole  = "user"

	// MIME Types supported by VertexAI.

	JSONMIMEType = "application/json"
	TextMIMEType = "text/plain"
)

func WithSystemPrompt(config *genai.GenerateContentConfig, prompt ...*genai.Part) *genai.GenerateContentConfig {
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}
	config.SystemInstruction = &genai.Content{
		Parts: prompt,
	}
	return config
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

func GenerateTextContent(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, prompt ...*genai.Part) (string, error) {
	contents := []*genai.Content{{Parts: prompt, Role: "user"}}
	resp, err := client.Models.GenerateContent(ctx, model, contents, config)
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
		if candidate.Content.Parts[0].Text != "" {
			return candidate.Content.Parts[0].Text, nil
		}
		return "", errors.New("part is not text")
	default:
		return "", errors.New("multiple response parts")
	}
}

// GenerateTypedContent extracts JSON data from text according to the provided schema.
func GenerateTypedContent(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, out any, prompt ...*genai.Part) error {
	if config == nil || config.ResponseSchema == nil {
		return errors.New("generate config must set a schema")
	}
	if config.ResponseMIMEType != JSONMIMEType {
		return errors.New("generate config must set a JSON MIME type")
	}
	text, err := GenerateTextContent(ctx, client, model, config, prompt...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return errors.Wrap(err, "parsing JSON response")
	}
	return nil
}
