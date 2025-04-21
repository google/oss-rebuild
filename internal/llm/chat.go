// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"
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
	// Notify is an optional channel to receive conversation turns.
	Notify chan<- genai.Content
}

// Chat manages the state of an ongoing conversation with a generative model,
// handling tool execution automatically.
type Chat struct {
	model     *genai.GenerativeModel
	session   *genai.ChatSession
	toolImpls map[string]Function
	maxIter   int
	notify    chan<- genai.Content
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
	var notify chan<- genai.Content
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
		notify = opts.Notify
	}
	session := model.StartChat()
	return &Chat{
		model:     model,
		session:   session,
		toolImpls: toolImpls,
		maxIter:   maxIter,
		notify:    notify,
	}, nil
}

// SendMessage sends a message to the model as part of the ongoing conversation.
// It handles the full turn logic, including executing any function calls requested
// by the model using the configured tools, and sends results back to the model
// until a final content response is received or an error occurs.
func (cm *Chat) SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error) {
	if len(parts) == 0 {
		return nil, errors.New("message parts cannot be empty")
	}
	currentParts := slices.Clone(parts)
	for range cm.maxIter {
		if cm.notify != nil {
			cm.notify <- *genai.NewUserContent(currentParts...)
		}
		resp, err := cm.session.SendMessage(ctx, currentParts...)
		if err != nil {
			return nil, errors.Wrap(err, "sending message")
		}
		if len(resp.Candidates) == 0 || resp.Candidates[0] == nil || resp.Candidates[0].Content == nil {
			feedback := "received nil or empty candidates/content"
			if resp.PromptFeedback != nil {
				feedback = fmt.Sprintf("prompt feedback: %+v", resp.PromptFeedback)
			}
			return resp, errors.New(feedback)
		}
		candidate := resp.Candidates[0]
		if cm.notify != nil && candidate.Content != nil {
			cm.notify <- *candidate.Content
		}
		if calls := candidate.FunctionCalls(); len(calls) > 0 {
			currentParts = currentParts[:0]
			for _, call := range calls {
				implFunc, found := cm.toolImpls[call.Name]
				if !found {
					return resp, errors.Errorf("tool implementation not found for function call '%s'", call.Name)
				}
				responsePart := genai.Part(implFunc(call.Args))
				currentParts = append(currentParts, responsePart)
			}
			continue
		} else if candidate.FinishReason == genai.FinishReasonStop {
			return resp, nil // graceful stop
		} else {
			return nil, errors.Errorf("chat stopped unexpectedly: [%s] %s", candidate.FinishReason, candidate.FinishMessage)
		}

	}
	return nil, errors.Errorf("maximum tool iterations (%d) exceeded", cm.maxIter)
}

// History returns a copy of the conversation history accumulated so far.
func (cm *Chat) History() []*genai.Content {
	return slices.Clone(cm.session.History)
}
