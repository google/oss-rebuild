// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// refMove records one source ref change the model asked for.
type refMove struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Asked  string    `json:"asked"` // the revision as the model named it
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// setSourceRefTool moves the ref the rebuild is made from. The ref is signed
// into the attestation, so it moves here, not in the build script.
func (a *defaultAgent) setSourceRefTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "set_source_ref",
			Description: "Change the commit the rebuild is made from. Use it when the evidence shows the release was cut from a different commit than the current source ref: the published gitHead or .cargo_vcs_info.json sha, a release tag, a version-bump commit. The next attempt clones the repository at that commit and re-infers the dependency phase there. Do not check out another commit in the build script instead; that is refused.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ref":    {Type: genai.TypeString, Description: "The commit sha, tag, or branch of the source repository to build from"},
					"reason": {Type: genai.TypeString, Description: "The evidence that the release was cut from this commit, in one or two sentences"},
				},
				Required: []string{"ref", "reason"},
			},
			Response: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"sha":    {Type: genai.TypeString, Description: "The full commit the source ref is now set to"},
					"error":  {Type: genai.TypeString, Description: "Why the ref could not be set, if it was not"},
					"result": {Type: genai.TypeString},
				},
			},
		},
		Function: func(args map[string]any) genai.FunctionResponse {
			ref, _ := args["ref"].(string)
			reason, _ := args["reason"].(string)
			resp := map[string]any{"sha": "", "error": "", "result": ""}
			sha, err := a.setSourceRef(ref, reason)
			if err != nil {
				resp["error"] = err.Error()
			} else {
				resp["sha"] = sha
				resp["result"] = fmt.Sprintf("source ref set to %s; the next proposal is built from it", sha)
			}
			return genai.FunctionResponse{Name: "set_source_ref", Response: resp}
		},
	}
}

// setSourceRef resolves ref in the session's repository and moves the agent's
// location to it.
func (a *defaultAgent) setSourceRef(ref, reason string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("ref is required")
	}
	if strings.TrimSpace(reason) == "" {
		return "", errors.New("reason is required: say what shows the release was cut from this commit")
	}
	if a.repo == nil {
		return "", errors.New("no repository is loaded")
	}
	hash, err := a.repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		// A publish commit is often never pushed (.cargo_vcs_info.json, gitHead), so
		// point at the nearest commit the repository has.
		return "", errors.Errorf("%q is not a revision of %s. A publish commit recorded in the artifact may never have been pushed; if so, call set_source_ref again with the nearest pushed commit whose tree matches the artifact (its pushed parent, or the release tag's parent), and reproduce any difference in the build", ref, a.loc.Repo)
	}
	if _, err := a.repo.CommitObject(*hash); err != nil {
		return "", errors.Wrapf(err, "%q does not name a commit", ref)
	}
	sha := hash.String()
	if sha == a.loc.Ref {
		return sha, errors.Errorf("%s is already the source ref", sha)
	}
	move := refMove{From: a.loc.Ref, To: sha, Asked: ref, Reason: reason, At: time.Now().UTC()}
	a.refMoves = append(a.refMoves, move)
	a.refMoved = true
	a.loc.Ref = sha
	log.Printf("source ref moved from %s to %s: %s", move.From, move.To, reason)
	return sha, nil
}

// reinferAfterMove re-infers the deps phase at a moved ref, since pypi's
// requirements come from the tree there. The old deps stay if inference fails.
func (a *defaultAgent) reinferAfterMove(ctx context.Context, opts *ProposeOpts, inst *rebuild.Instructions) {
	if !a.refMoved {
		return
	}
	a.refMoved = false
	a.uploadRecord(ctx, opts, "0-refmove.json", a.refMoves)
	moved, err := a.reinfer(ctx, a.loc)
	if err != nil {
		// TODO: Return the error so the model learns the deps are still the old ref's.
		log.Printf("re-inferring deps at %s: %v", a.loc.Ref, err)
		return
	}
	inst.Deps, inst.Requires = moved.Deps, moved.Requires
	if moved.OutputPath != "" {
		inst.OutputPath = moved.OutputPath
	}
}

// inferFunc infers the build at loc and renders its instructions.
type inferFunc func(context.Context, rebuild.Location) (rebuild.Instructions, error)

// inferAt runs heuristic inference pinned to loc and returns the rendered
// instructions, whose deps phase belongs to that commit.
func (a *defaultAgent) inferAt(ctx context.Context, loc rebuild.Location) (rebuild.Instructions, error) {
	req := schema.InferenceRequest{
		Ecosystem: a.t.Ecosystem,
		Package:   a.t.Package,
		Version:   a.t.Version,
		Artifact:  a.t.Artifact,
		StrategyHint: &schema.StrategyOneOf{
			LocationHint: &rebuild.LocationHint{Location: rebuild.Location{Repo: loc.Repo, Ref: loc.Ref, Dir: loc.Dir}},
		},
	}
	s, err := inferenceservice.Infer(ctx, req, &inferenceservice.InferDeps{
		HTTPClient:         a.deps.RegistryClient,
		GitCache:           a.deps.GitCache,
		CratesRegistryStub: a.deps.CratesRegistryStub,
		RepoOptF: func() *gitx.RepositoryOptions {
			return &gitx.RepositoryOptions{Worktree: memfs.New(), Storer: gitx.NewInMemoryStorer()}
		},
	})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	st, err := s.Strategy()
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return st.GenerateFor(a.t, a.buildEnv())
}
