package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
)

var execCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd = exec.CommandContext

func TestDockerExecutor_Run(t *testing.T) {
	ctx := context.Background()
	dockerfile := []byte("FROM alpine:latest\nRUN mkdir /out\nRUN echo 'Hello, World!' > /out/hello.txt")
	pkgName := "testpkg"
	version := "1.0.0"

	t.Run("successful run", func(t *testing.T) {
		executor, err := NewDockerExecutor()
		if err != nil {
			t.Fatalf("failed to create DockerExecutor: %v", err)
		}
		defer executor.Cleanup()

		// Mock exec.CommandContext to simulate successful docker commands
		execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cs := []string{"-test.run=TestHelperProcess", "--", name}
			cs = append(cs, args...)
			cmd := exec.Command(os.Args[0], cs...)
			cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
			return cmd
		}
		defer func() { execCommandContext = exec.CommandContext }()

		err = executor.Run(ctx, dockerfile, pkgName, version)
		if err != nil {
			t.Fatalf("Run() failed: %v", err)
		}
	})

}

func TestDockerExecutor_Setup(t *testing.T) {
	ctx := context.Background()
	projectFs := memfs.New()
	artifact := "testfile.txt"

	// Create a test file in the mock filesystem
	file, err := projectFs.Create(filepath.Join("upstream", artifact))
	if err != nil {
		t.Fatalf("failed to create test file in mock filesystem: %v", err)
	}
	_, err = file.Write([]byte("test content"))
	if err != nil {
		t.Fatalf("failed to write to test file: %v", err)
	}
	file.Close()

	t.Run("successful setup", func(t *testing.T) {
		executor, err := NewDockerExecutor()
		if err != nil {
			t.Fatalf("failed to create DockerExecutor: %v", err)
		}
		defer executor.Cleanup()

		err = executor.Setup(ctx, projectFs, artifact)
		if err != nil {
			t.Fatalf("Setup() failed: %v", err)
		}

		// Verify the file was copied to the correct location
		dstPath := filepath.Join(executor.root, "upstream", artifact)
		_, err = os.Stat(dstPath)
		if os.IsNotExist(err) {
			t.Fatalf("expected file %s to exist, but it does not", dstPath)
		}
	})

	t.Run("missing source file", func(t *testing.T) {
		executor, err := NewDockerExecutor()
		if err != nil {
			t.Fatalf("failed to create DockerExecutor: %v", err)
		}
		defer executor.Cleanup()

		err = executor.Setup(ctx, projectFs, "nonexistent.txt")
		if err == nil || !strings.Contains(err.Error(), "failed to open source file") {
			t.Fatalf("expected error for missing source file, got: %v", err)
		}
	})
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}
