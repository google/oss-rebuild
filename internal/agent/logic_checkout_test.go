// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestInitializeFromIterationRejectsInvalidRef(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("fixture", &git.CommitOptions{Author: &object.Signature{
		Name: "fixture", Email: "fixture@example.com", When: time.Unix(1, 0),
	}}); err != nil {
		t.Fatal(err)
	}
	bareDir := filepath.Join(filepath.Dir(repoDir), "repo.git")
	cmd := exec.Command("git", "clone", "--bare", repoDir, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", bareDir, "update-server-info")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-server-info: %v\n%s", err, out)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(repoDir))))
	defer server.Close()

	strategy := schema.NewStrategyOneOf(&rebuild.ManualStrategy{Location: rebuild.Location{
		Repo: server.URL + "/repo.git",
		Ref:  strings.Repeat("f", 40),
	}})
	iteration := &schema.AgentIteration{Strategy: &strategy}
	agent := NewDefaultAgent(rebuild.Target{Package: "fixture"}, nil)

	err = agent.InitializeFromIteration(context.Background(), iteration)
	if err == nil {
		t.Fatal("InitializeFromIteration succeeded with a ref absent from the repository")
	}
	if !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("InitializeFromIteration returned an unrelated error: %v", err)
	}
}
