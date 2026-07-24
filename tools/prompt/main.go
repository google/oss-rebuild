// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/llm"
	"google.golang.org/genai"
)

var (
	project    = flag.String("project", "", "Google Cloud project ID")
	repo       = flag.String("repo", "", "Path to local git repository")
	prompt     = flag.String("prompt", "", "Prompt text")
	promptFile = flag.String("prompt-file", "", "Path to a file containing the prompt")
	baseModel  = flag.String("model", llm.GeminiPro, fmt.Sprintf("Base model to use (options: %s, %s)", llm.GeminiPro, llm.GeminiFlash))
)

func main() {
	flag.Parse()

	if *project == "" {
		log.Fatal("project is required")
	}
	if *repo == "" {
		log.Fatal("repo is required")
	}
	if *prompt == "" && *promptFile == "" {
		log.Fatal("either prompt or prompt-file is required")
	}
	if *prompt != "" && *promptFile != "" {
		log.Fatal("cannot specify both prompt and prompt-file")
	}

	ctx := context.Background()

	// Currently only support local paths.
	var gitRepo *git.Repository
	var err error
	if _, err := os.Stat(*repo); err == nil {
		gitRepo, err = git.PlainOpen(*repo)
		if err != nil {
			log.Fatalf("opening local repo: %v", err)
		}
	} else {
		log.Fatalf("repo not found or not local: %v", err)
	}
	head, err := gitRepo.Head()
	if err != nil {
		log.Fatalf("getting HEAD ref: %v", err)
	}
	ref := head.Hash().String()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  *project,
		Location: "us-central1",
	})
	if err != nil {
		log.Fatalf("creating AI client: %v", err)
	}
	config := &genai.GenerateContentConfig{
		Temperature:     genai.Ptr[float32](.1),
		MaxOutputTokens: 16000,
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: "AUTO"},
		},
	}

	tools := llm.GitTools(func() (*git.Repository, string) { return gitRepo, ref })
	chat, err := llm.NewChat(ctx, client, *baseModel, config, &llm.ChatOpts{Tools: tools})
	if err != nil {
		log.Fatalf("creating chat: %v", err)
	}

	promptContent := *prompt
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			log.Fatalf("reading prompt file: %v", err)
		}
		promptContent = string(b)
	}

	fmt.Printf("Running prompt against repo %s (ref: %s)...\n", *repo, ref)

	part := genai.NewPartFromText(promptContent)
	var response strings.Builder
	for content, err := range chat.SendMessageStream(ctx, part) {
		if err != nil {
			log.Fatalf("sending message: %v", err)
		}
		for _, p := range content.Parts {
			if p.Text != "" {
				response.WriteString(p.Text)
			}
		}
	}

	var totalUsage, promptUsage, candidatesUsage, cachedUsage int32
	for _, u := range chat.Usage() {
		if u != nil {
			promptUsage += u.PromptTokenCount
			candidatesUsage += u.CandidatesTokenCount
			totalUsage += u.TotalTokenCount
			cachedUsage += u.CachedContentTokenCount
		}
	}
	fmt.Printf("Token Usage: Total %d (Prompt %d, Candidates %d, Cached %d)\n", totalUsage, promptUsage, candidatesUsage, cachedUsage)

	fmt.Println("--- Response ---")
	fmt.Println(response.String())
}
