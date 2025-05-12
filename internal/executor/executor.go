package executor

import (
	"archive/tar"
	"context"
	"fmt"
	"github.com/google/oss-rebuild/internal/textwrap"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type DockerExecutor struct {
	packageName string
	containerID string
	client      *client.Client
	mutex       sync.Mutex
}

// NewDockerExecutor creates a new DockerExecutor for a specific package.
func NewDockerExecutor(packageName string) (*DockerExecutor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %v", err)
	}

	return &DockerExecutor{
		packageName: packageName,
		client:      cli,
	}, nil
}

// StartContainer starts a Docker container for the package if not already running.
func (d *DockerExecutor) StartContainer(ctx context.Context, strategy rebuild.Strategy, target rebuild.Target) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.containerID != "" {
		// Container is already running
		return nil
	}

	instructions, err := strategy.GenerateFor(target, rebuild.BuildEnv{})
	if err != nil {
		return fmt.Errorf("failed to generate build instructions: %v", err)
	}
	//FROM ghcr.io/awesome-containers/alpine-build-essential:latest
	dockerfile := fmt.Sprintf(`
FROM docker.io/library/alpine:3.19
RUN apk add %s
RUN git clone %s %s
WORKDIR %s
`, strings.Join(instructions.SystemDeps, " "), instructions.Location.Repo, d.packageName, d.packageName)
	// Dynamically create a Dockerfile
	dockerfile = textwrap.Dedent(dockerfile)

	// Build the Docker image
	imageName := fmt.Sprintf("%s", d.packageName)
	buildContext, err := createBuildContext(dockerfile)
	if err != nil {
		return fmt.Errorf("failed to create build context: %v", err)
	}
	defer buildContext.Close()

	buildOptions := types.ImageBuildOptions{
		Tags:       []string{imageName},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	buildResponse, err := d.client.ImageBuild(ctx, buildContext, buildOptions)
	if err != nil {
		return fmt.Errorf("failed to build Docker image: %v", err)
	}
	defer buildResponse.Body.Close()

	// Consume the build output to ensure the image is fully built
	if _, err := io.Copy(os.Stdout, buildResponse.Body); err != nil {
		return fmt.Errorf("failed to read build output: %v", err)
	}

	// Start the container
	resp, err := d.client.ContainerCreate(ctx, &container.Config{
		Image: imageName,
		Cmd:   []string{"tail", "-f", "/dev/null"}, // Keep the container running
	}, nil, nil, nil, "")
	if err != nil {
		return fmt.Errorf("failed to create Docker container: %v", err)
	}

	d.containerID = resp.ID
	if err := d.client.ContainerStart(ctx, d.containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start Docker container: %v", err)
	}

	return nil
}

// ExecuteWithStrategy checks out the correct commit and executes the build inside the container.
func (d *DockerExecutor) ExecuteWithStrategy(ctx context.Context, strategy rebuild.Strategy, target rebuild.Target) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.containerID == "" {
		return fmt.Errorf("container is not running for package %s", d.packageName)
	}

	// Generate build instructions from the strategy
	instructions, err := strategy.GenerateFor(target, rebuild.BuildEnv{})
	if err != nil {
		return fmt.Errorf("failed to generate build instructions: %v", err)
	}

	if err = d.executeCommand(ctx, []string{"sh", "-c", instructions.Deps}); err != nil {
	}
	// Checkout the correct commit
	checkoutCmd := []string{"git", "checkout", "--force", instructions.Location.Ref}
	if err := d.executeCommand(ctx, checkoutCmd); err != nil {
		return fmt.Errorf("failed to checkout commit: %v", err)
	}

	// Execute the build commands

	if err := d.executeCommand(ctx, []string{"sh", "-c", instructions.Build}); err != nil {
		return fmt.Errorf("build command failed: %v", err)
	}

	return nil
}

// executeCommand runs a command inside the Docker container.
func (d *DockerExecutor) executeCommand(ctx context.Context, command []string) error {
	execConfig := container.ExecOptions{
		Cmd:          command,
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err := d.client.ContainerExecCreate(ctx, d.containerID, execConfig)
	if err != nil {
		return fmt.Errorf("failed to create exec instance: %v", err)
	}

	resp, err := d.client.ContainerExecAttach(ctx, execIDResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("failed to attach to exec instance: %v", err)
	}
	defer resp.Close()

	// Stream the output
	_, err = io.Copy(os.Stdout, resp.Reader)
	return err
}

// StopContainer stops and removes the Docker container.
func (d *DockerExecutor) StopContainer(ctx context.Context) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.containerID == "" {
		return nil
	}

	if err := d.client.ContainerStop(ctx, d.containerID, container.StopOptions{}); err != nil {
		return fmt.Errorf("failed to stop Docker container: %v", err)
	}

	if err := d.client.ContainerRemove(ctx, d.containerID, container.RemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove Docker container: %v", err)
	}

	d.containerID = ""
	return nil
}

// Helper function to create a Docker build context
func createBuildContext(dockerfile string) (io.ReadCloser, error) {
	// Create a tar archive containing the Dockerfile
	tarFile, err := os.CreateTemp("", "docker-build-context-*.tar")
	if err != nil {
		return nil, err
	}
	defer tarFile.Close()

	tarWriter := tar.NewWriter(tarFile)
	defer tarWriter.Close()

	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfile)),
	}); err != nil {
		return nil, err
	}
	if _, err := tarWriter.Write([]byte(dockerfile)); err != nil {
		return nil, err
	}

	return os.Open(tarFile.Name())
}
