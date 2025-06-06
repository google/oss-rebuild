// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package assistant

import (
	"context"

	"cloud.google.com/go/vertexai/genai"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/tools/ctl/ide/chatbox"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
)

// Session represents one ongoing conversation with the assistant.
type Session interface {
	chatbox.ChatBackend
}

// Assistant provides sessions, debugging different contexts.
type Assistant interface {
	Session(context.Context, rundex.Rebuild) (Session, error)
}

// cmdFunc SHOULD NOT close the out channel. The invoking code may still use the out channel after the cmdFunc has completed.
// Also, cmdFunc should only return once it no longer needs the `out` channel, as it will be closed soon after.
type cmdFunc func(ctx context.Context, out chan<- *chatbox.Message) error

type assistant struct {
	butler localfiles.Butler
	model  *genai.GenerativeModel
}

var _ Assistant = (*assistant)(nil)

func NewAssistant(butler localfiles.Butler, model *genai.GenerativeModel) *assistant {
	return &assistant{
		butler: butler,
		model:  model,
	}
}

func (a *assistant) Session(ctx context.Context, attempt rundex.Rebuild) (Session, error) {
	chat, err := llm.NewChat(a.model, &llm.ChatOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "creating chat")
	}
	return newSession(a.butler, chat, attempt), nil
}
