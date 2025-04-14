package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
)

type DockerExecutor struct {
	fs   billy.Filesystem
	root string
}

func NewDockerExecutor() (*DockerExecutor, error) {
	tmpDir, err := os.MkdirTemp("", "docker-build-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %v", err)
	}

	return &DockerExecutor{
		fs:   memfs.New(),
		root: tmpDir,
	}, nil
}

func (d *DockerExecutor) Setup(ctx context.Context, projectFs billy.Filesystem, artifact string) error {
	// Create necessary directories
	if err := os.MkdirAll(filepath.Join(d.root, "upstream"), 0755); err != nil {
		return fmt.Errorf("failed to create upstream dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(d.root, "out"), 0755); err != nil {
		return fmt.Errorf("failed to create out dir: %v", err)
	}

	// Copy artifact to the Docker context
	srcFile, err := projectFs.Open(filepath.Join("upstream", artifact))
	if err != nil {
		return fmt.Errorf("failed to open source file: %v", err)
	}
	defer srcFile.Close()

	dstPath := filepath.Join(d.root, "upstream", artifact)
	dstFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %v", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file: %v", err)
	}

	return nil
}

func (d *DockerExecutor) Run(ctx context.Context, dockerfile []byte, pkgName, version string) error {
	// Write Dockerfile
	dockerfilePath := filepath.Join(d.root, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, dockerfile, 0644); err != nil {
		return fmt.Errorf("failed to write Dockerfile: %v", err)
	}

	// Build Docker image
	imageTag := fmt.Sprintf("%s-%s:%s", pkgName, version, "build")
	buildCmd := exec.CommandContext(ctx, "docker", "buildx", "build", "--no-cache", "-f", dockerfilePath, "-t", imageTag, d.root)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %v", err)
	}

	// Run Docker container
	runCmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-v", fmt.Sprintf("%s:/out", filepath.Join(d.root, "out")), imageTag)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Run(); err != nil {
		return fmt.Errorf("docker run failed: %v", err)
	}

	return nil
}

func (d *DockerExecutor) Cleanup() error {
	return os.RemoveAll(d.root)
}
