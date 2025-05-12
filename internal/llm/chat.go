// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"
	"iter"
	"slices"

	"cloud.google.com/go/vertexai/genai"
	"github.com/pkg/errors"
)

const defaultMaxToolIterations = 10

// ChatOpts provides configuration options for creating a new Chat instance.
type ChatOpts struct {
	// Tools defines the set of function definitions (declaration + implementation)
	// available for this chat execution. The declarations must match those
	// configured in the model passed to ExecuteChat.
	Tools []*FunctionDefinition
	// MaxIterations sets the maximum number of send/receive cycles.
	// If zero or less, a default value (defaultMaxToolIterations) is used.
	MaxToolIterations int
}

// Chat manages the state of an ongoing conversation with a generative model,
// handling tool execution automatically.
type Chat struct {
	model     *genai.GenerativeModel
	session   *genai.ChatSession
	toolImpls map[string]Function
	maxIter   int
}

// NewChat creates and initializes a new Chat instance for managing a conversation.
// It configures the underlying session with the provided model and options,
// including tool definitions.
func NewChat(model *genai.GenerativeModel, opts *ChatOpts) (*Chat, error) {
	if model == nil {
		return nil, errors.New("model cannot be nil")
	}
	maxIter := defaultMaxToolIterations
	toolImpls := make(map[string]Function)
	if opts != nil {
		if opts.MaxToolIterations > 0 {
			maxIter = opts.MaxToolIterations
		}
		model = WithTools(*model, opts.Tools)
		for _, def := range opts.Tools {
			if def != nil {
				if def.Function == nil {
					return nil, errors.Errorf("tool '%s' provided without an implementation Function", def.Name)
				}
				toolImpls[def.Name] = def.Function
			}
		}
	}
	session := model.StartChat()
	return &Chat{
		model:     model,
		session:   session,
		toolImpls: toolImpls,
		maxIter:   maxIter,
	}, nil
}

// SendMessage sends a message to the model as part of the ongoing conversation.
// It handles the full turn logic, including executing any function calls requested
// by the model using the configured tools, and sends results back to the model
// until a final content response is received or an error occurs.
func (cm *Chat) SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error) {
	var lastContent *genai.Content
	stream := cm.SendMessageStream(ctx, parts...)
	for c, err := range stream {
		if err != nil {
			return nil, err
		}
		lastContent = c
	}
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      lastContent,
			FinishReason: genai.FinishReasonStop,
		}},
	}, nil
}

// SendMessageStream sends a message to the model as part of the ongoing conversation.
// It handles the full turn logic, including executing any function calls
// requested by the model using the configured tools, and yields each Content
// object until the model produces a final response or an error occurs.
func (cm *Chat) SendMessageStream(ctx context.Context, parts ...genai.Part) iter.Seq2[*genai.Content, error] {
	return func(yield func(*genai.Content, error) bool) {
		if len(parts) == 0 {
			yield(nil, errors.New("message parts cannot be empty"))
			return
		}
		currentParts := slices.Clone(parts)
		for range cm.maxIter {
			if !yield(genai.NewUserContent(currentParts...), nil) {
				return
			}
			resp, err := cm.session.SendMessage(ctx, currentParts...)
			if err != nil {
				yield(nil, errors.Wrap(err, "sending message"))
				return
			}
			if len(resp.Candidates) == 0 || resp.Candidates[0] == nil || resp.Candidates[0].Content == nil {
				feedback := "received nil or empty candidates/content"
				if resp.PromptFeedback != nil {
					feedback = fmt.Sprintf("prompt feedback: %+v", resp.PromptFeedback)
				}
				yield(nil, errors.New(feedback))
				return
			}
			candidate := resp.Candidates[0]
			if !yield(candidate.Content, nil) {
				return
			}
			if calls := candidate.FunctionCalls(); len(calls) > 0 {
				currentParts = currentParts[:0]
				for _, call := range calls {
					implFunc, found := cm.toolImpls[call.Name]
					if !found {
						yield(nil, errors.Errorf("tool implementation not found for function call '%s'", call.Name))
						return
					}
					responsePart := genai.Part(implFunc(call.Args))
					currentParts = append(currentParts, responsePart)
				}
				continue
			} else if candidate.FinishReason == genai.FinishReasonStop {
				return
			} else {
				yield(nil, errors.Errorf("chat stopped unexpectedly: [%s] %s", candidate.FinishReason, candidate.FinishMessage))
				return
			}
		}
		yield(nil, errors.Errorf("maximum tool iterations (%d) exceeded", cm.maxIter))
		return
	}
}

// History returns a copy of the conversation history accumulated so far.
func (cm *Chat) History() []*genai.Content {
	return slices.Clone(cm.session.History)
}
