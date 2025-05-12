// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"fmt"
	"strings"

	"cloud.google.com/go/vertexai/genai"
)

func FormatContent(content genai.Content) string {
	msg := fmt.Sprintf("--- Role: %s ---\n", content.Role)
	if len(content.Parts) == 0 {
		msg += "  (Empty content)"
	} else {
		for _, part := range content.Parts {
			msg += fmt.Sprintf("\n>>> Type: %T\n\n", part)
			switch part.(type) {
			case genai.Text:
				s := string(part.(genai.Text))
				msg += "  " + strings.ReplaceAll(s, "\n", "\n  ")
			case genai.FunctionCall:
				call := part.(genai.FunctionCall)
				msg += fmt.Sprintf("%s(%v)", call.Name, call.Args)
			case genai.FunctionResponse:
				resp := part.(genai.FunctionResponse)
				msg += fmt.Sprintf("%s(...) => %v", resp.Name, resp.Response)
			default:
				msg += "<unprintable type>"
			}
		}
	}
	return msg
}
