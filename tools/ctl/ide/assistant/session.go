// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package assistant

import (
	"context"
	"fmt"
	"os"
	"strings"

	"cloud.google.com/go/vertexai/genai"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/google/oss-rebuild/tools/ctl/ide/chatbox"
	"github.com/google/oss-rebuild/tools/ctl/ide/tmux"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
)

const (
	geminiIdentity   = chatbox.Identity("gemini")
	uploadBytesLimit = 10000
)

type session struct {
	butler  localfiles.Butler
	chat    *llm.Chat
	attempt rundex.Rebuild
	cmds    map[string]cmdFunc
}

var _ Session = (*session)(nil)

func newSession(butler localfiles.Butler, chat *llm.Chat, attempt rundex.Rebuild) *session {
	a := &session{
		butler:  butler,
		chat:    chat,
		attempt: attempt,
	}
	a.cmds = map[string]cmdFunc{
		"exit": func(ctx context.Context, out chan<- *chatbox.Message) error {
			return chatbox.ErrCloseChat
		},
		"logs": func(ctx context.Context, out chan<- *chatbox.Message) error {
			logs, err := a.butler.Fetch(context.Background(), attempt.RunID, attempt.WasSmoketest(), rebuild.DebugLogsAsset.For(attempt.Target()))
			if err != nil {
				return errors.Wrap(err, "downloading logs")
			}
			if err := tmux.Start(fmt.Sprintf("cat %s | less", logs)); err != nil {
				return errors.Wrap(err, "failed to read logs")
			}
			return nil
		},
		"diff": func(ctx context.Context, out chan<- *chatbox.Message) error {
			diff, err := a.butler.Fetch(context.Background(), attempt.RunID, attempt.WasSmoketest(), diffoscope.DiffAsset.For(attempt.Target()))
			if err != nil {
				return errors.Wrap(err, "generating diff")
			}
			if err := tmux.Start(fmt.Sprintf("cat %s | less", diff)); err != nil {
				return errors.Wrap(err, "failed to read diff")
			}
			return nil
		},
		"debug": a.debug,
		// TODO: Propose changes
		// TODO: Implement /help and /?
	}
	return a
}

func (a *session) HandleInput(ctx context.Context, in string, out chan<- *chatbox.Message) error {
	defer close(out)
	if len(in) == 0 {
		return nil
	} else if in[0] == '/' {
		if fn, ok := a.cmds[strings.ToLower(in[1:])]; ok {
			out <- &chatbox.Message{Who: chatbox.User, Content: in}
			err := fn(ctx, out)
			if errors.Is(err, chatbox.ErrCloseChat) {
				return err
			} else if err != nil {
				out <- &chatbox.Message{Who: chatbox.System, Content: fmt.Sprintf("Command error: %v", err)}
			}
		} else {
			out <- &chatbox.Message{Who: chatbox.System, Content: "Unrecognized command: \"" + in + "\""}
		}
		return nil
	} else {
		out <- &chatbox.Message{Who: chatbox.User, Content: in}
		contentParts := []genai.Part{genai.Text(in)}
		for content, err := range a.chat.SendMessageStream(context.Background(), contentParts...) {
			if content.Role == "user" {
				continue
			}
			if err != nil {
				out <- &chatbox.Message{Who: chatbox.System, Content: errors.Wrap(err, "sending message").Error()}
				break
			}
			out <- &chatbox.Message{Who: geminiIdentity, Content: formatContent(content)}
		}
		return nil
	}
}

type attemptEvidence struct {
	metadata string
	builddef string
	logs     string
	diff     string
}

func (a *session) evidence(ctx context.Context, attempt rundex.Rebuild) *attemptEvidence {
	var metadata string
	{
		metadata = fmt.Sprintf("%+v:%s", attempt.Target(), attempt.Message)
	}
	var builddef string
	{
		s, err := attempt.Strategy.Strategy()
		if err != nil {
			builddef = errors.Wrap(err, "unpacking StrategyOneOf").Error()
		} else {
			builddef, err = rebuild.MakeDockerfile(rebuild.Input{Strategy: s, Target: attempt.Target()}, rebuild.RemoteOptions{})
			if err != nil {
				builddef = errors.Wrap(err, "making dockerfile").Error()
			}
		}
	}
	var logs string
	{
		path, err := a.butler.Fetch(context.Background(), attempt.RunID, attempt.WasSmoketest(), rebuild.DebugLogsAsset.For(attempt.Target()))
		if err != nil {
			logs = errors.Wrap(err, "downloading logs").Error()
		} else {
			if content, err := os.ReadFile(path); err != nil {
				logs = errors.Wrap(err, "reading logs").Error()
			} else {
				logs = string(content)
			}
		}
		if len(logs) > uploadBytesLimit {
			offset := len(logs) - uploadBytesLimit
			logs = fmt.Sprintf("...(truncated %d bytes)...\n%s", offset, logs[offset:])
		}
	}
	var diff string
	{
		path, err := a.butler.Fetch(context.Background(), attempt.RunID, attempt.WasSmoketest(), diffoscope.DiffAsset.For(attempt.Target()))
		if err != nil {
			diff = errors.Wrap(err, "generating diff").Error()
		} else {
			if content, err := os.ReadFile(path); err != nil {
				diff = errors.Wrap(err, "reading diff file").Error()
			} else {
				diff = string(content)
			}
		}
		if len(diff) > uploadBytesLimit {
			offset := len(diff) - uploadBytesLimit
			diff = fmt.Sprintf("...(truncated %d bytes)...\n%s", offset, diff[offset:])
		}
	}
	return &attemptEvidence{metadata: metadata, builddef: builddef, logs: logs, diff: diff}
}

func (a *session) debug(ctx context.Context, out chan<- *chatbox.Message) error {
	evidence := a.evidence(ctx, a.attempt)
	contentParts := []genai.Part{
		genai.Text((`Here are the details from my most recent attempt to rebuild. In order they are:
1: Metadata about the rebuild attempt
2: The build definition
3: The build logs
4: The diff between our rebuilt package and the upstream

Please briefly answer the following questions, keeping your answer limited to two sentences.

1) Is there anything interesting about the build instructions?
2) Were there any errors in the logs? What caused them?
3) What caused the diff in the output artifact?
4) What should we change in our instructions to fix the rebuild and resolve any diffs?`)),
		genai.Text(evidence.metadata),
		genai.Text(evidence.builddef),
		genai.Text(evidence.logs),
		genai.Text(evidence.diff),
	}
	out <- &chatbox.Message{Who: chatbox.System, Content: "Sending a debug request including metadata, build def, logs, and diff"}
	for content, err := range a.chat.SendMessageStream(ctx, contentParts...) {
		if err != nil {
			out <- &chatbox.Message{Who: chatbox.System, Content: errors.Wrap(err, "Sending message").Error()}
			break
		}
		out <- &chatbox.Message{Who: geminiIdentity, Content: formatContent(content)}
	}
	return nil
}

func formatContent(content *genai.Content) string {
	msg := fmt.Sprintf("--- Role: %s ---\n", content.Role)
	if len(content.Parts) == 0 {
		msg += "  (Empty content)"
	} else {
		for i, part := range content.Parts {
			if content.Role == "user" && i > 0 {
				break
			}
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
