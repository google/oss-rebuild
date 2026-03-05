// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"google.golang.org/genai"
)

func FormatContent(content genai.Content) string {
	msg := fmt.Sprintf("--- Role: %s ---\n", content.Role)
	if len(content.Parts) == 0 {
		msg += "  (Empty content)"
	} else {
		for _, part := range content.Parts {
			msg += fmt.Sprintf("\n>>> Type: %T\n\n", part)
			if part.Text != "" {
				s := part.Text
				msg += "  " + strings.ReplaceAll(s, "\n", "\n  ")
			} else if part.FunctionCall != nil {
				call := part.FunctionCall
				msg += fmt.Sprintf("%s(%v)", call.Name, call.Args)
			} else if part.FunctionResponse != nil {
				resp := part.FunctionResponse
				msg += fmt.Sprintf("%s(...) => %v", resp.Name, resp.Response)
			} else {
				msg += "<unprintable type>"
			}
		}
	}
	var c func(s string, a ...interface{}) string
	switch content.Role {
	case ModelRole:
		c = color.BlueString
	case UserRole:
		c = color.YellowString
	default:
		c = color.WhiteString
	}
	return c(msg)
}
