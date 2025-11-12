package pypi

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// NOTE: Requires removal of the testFiles/ prefix in utils.go to work properly
// This will be found in a comment line

func TestCorrectSubPyproject(t *testing.T) {
	ctx := context.Background()

	// Load test repository NOTE - This can be changed
	repoPath := os.Getenv("REPO_PATH")
	if repoPath == "" {
		t.Fatal("Failed to specify 'REPO_PATH' in environment")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("Failed to open git repository at %s: %v", repoPath, err)
	}

	refStr := os.Getenv("COMMIT_REF")
	if refStr == "" {
		t.Fatal("Failed to specify 'COMMIT_REF' in environment")
	}
	ref := plumbing.NewHash(refStr)
	commit, err := repo.CommitObject(ref)
	if err != nil {
		t.Fatalf("Failed to load git commit from path %s: %v", repoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Failed to load git tree from commit: %v", err)
	}

	reqs, err := ExtractAllRequirements(ctx, tree, "pygad", "3.5.0")
	if err != nil {
		t.Fatalf("Failed to extract requirements: %v", err)
	}

	expectedReqs := []string{"setuptools"}
	if len(reqs) != len(expectedReqs) {
		t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
	}

	for _, expected := range expectedReqs {
		found := false
		for _, req := range reqs {
			if req == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected requirement %s not found in extracted requirements", expected)
		}
	}
}

func TestMainPyproject(t *testing.T) {
	ctx := context.Background()

	// Load test repository NOTE - This can be changed
	repoPath := os.Getenv("REPO_PATH")
	if repoPath == "" {
		t.Fatal("Failed to specify 'REPO_PATH' in environment")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("Failed to open git repository at %s: %v", repoPath, err)
	}

	refStr := os.Getenv("COMMIT_REF")
	if refStr == "" {
		t.Fatal("Failed to specify 'COMMIT_REF' in environment")
	}
	ref := plumbing.NewHash(refStr)
	commit, err := repo.CommitObject(ref)
	if err != nil {
		t.Fatalf("Failed to load git commit from path %s: %v", repoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Failed to load git tree from commit: %v", err)
	}

	reqs, err := ExtractAllRequirements(ctx, tree, "unknown", "1.2.3")
	if err != nil {
		t.Fatalf("Failed to extract requirements: %v", err)
	}

	expectedReqs := []string{"setuptools>=61.0.0"}
	if len(reqs) != len(expectedReqs) {
		t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
	}

	for _, expected := range expectedReqs {
		found := false
		for _, req := range reqs {
			if req == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected requirement %s not found in extracted requirements", expected)
		}
	}
}

func TestPoetryPyproject(t *testing.T) {
	// For now, the result will be empty cause that is the commit
	// I will change it later

	ctx := context.Background()

	// Load test repository NOTE - This can be changed
	repoPath := os.Getenv("REPO_PATH")
	if repoPath == "" {
		t.Fatal("Failed to specify 'REPO_PATH' in environment")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("Failed to open git repository at %s: %v", repoPath, err)
	}

	refStr := os.Getenv("COMMIT_REF")
	if refStr == "" {
		t.Fatal("Failed to specify 'COMMIT_REF' in environment")
	}
	ref := plumbing.NewHash(refStr)
	commit, err := repo.CommitObject(ref)
	if err != nil {
		t.Fatalf("Failed to load git commit from path %s: %v", repoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Failed to load git tree from commit: %v", err)
	}

	reqs, err := ExtractAllRequirements(ctx, tree, "msteamsapi", "0.9.5")
	if err != nil {
		t.Fatalf("Failed to extract requirements: %v", err)
	}

	expectedReqs := []string{"poetry-core"}
	if len(reqs) != len(expectedReqs) {
		log.Println(reqs)
		log.Println("---")
		log.Println(expectedReqs)
		log.Println("---")
		t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
	}

	for _, expected := range expectedReqs {
		found := false
		for _, req := range reqs {
			if req == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected requirement %s not found in extracted requirements", expected)
		}
	}
}
