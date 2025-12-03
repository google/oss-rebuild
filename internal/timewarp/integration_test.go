// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package timewarp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// getUserID returns "uid:gid" for running docker containers as current user
func getUserID() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

// startTimewarpServer starts an in-process timewarp HTTP server and returns the port and cleanup function
func startTimewarpServer(t *testing.T) (int, func()) {
	t.Helper()
	// Create handler with default HTTP client
	handler := &Handler{
		Client: http.DefaultClient,
	}
	// Find an available port - bind to all interfaces so Docker can reach it
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Start server
	server := &http.Server{
		Handler: handler,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Logf("Server error: %v", err)
		}
	}()
	// Wait for server to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Timewarp server failed to start within timeout")
		default:
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				// Server is ready
				cleanup := func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := server.Shutdown(ctx); err != nil {
						t.Logf("Server shutdown error: %v", err)
					}
				}
				return port, cleanup
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// runDockerCommand runs a docker command and returns stdout, stderr, and error
func runDockerCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Logf("Running: docker %s", strings.Join(args, " "))
	cmd := exec.Command("docker", args...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if err != nil {
		t.Logf("Docker command failed: %v\nStdout: %s\nStderr: %s", err, stdout, stderr)
	} else {
		t.Logf("Docker stdout: %s", stdout)
		if stderr != "" {
			t.Logf("Docker stderr: %s", stderr)
		}
	}
	return stdout, stderr, err
}

// checkDockerAvailable checks if docker is available and skips the test if not
func checkDockerAvailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		t.Skip("Docker is not available, skipping integration test")
	}
}

// TestTimewarpNPMClientVersions tests that timewarp works correctly across different npm versions
func TestTimewarpNPMClientVersions(t *testing.T) {
	checkDockerAvailable(t)
	port, cleanup := startTimewarpServer(t)
	t.Cleanup(cleanup)
	// Test same package at same timestamp across different npm versions
	// All should resolve to express 4.17.1 (latest as of 2020-01-01)
	timestamp := "2020-01-01T00:00:00Z"
	expectedVersion := "4.17.1"
	images := []struct {
		image       string
		description string
	}{
		{"node:14-alpine", "npm 6.x"},
		{"node:16-alpine", "npm 8.x"},
		{"node:18-alpine", "npm 9.x"},
		{"node:20-alpine", "npm 10.x"},
	}
	for _, img := range images {
		t.Run(img.description, func(t *testing.T) {
			t.Parallel()
			registryURL := fmt.Sprintf("http://npm:%s@127.0.0.1:%d", timestamp, port)
			tempDir := t.TempDir()
			dockerArgs := []string{
				"run",
				"--rm",
				"--network=host",
				"--user", getUserID(),
				"-v", fmt.Sprintf("%s:/workspace", tempDir),
				"-w", "/workspace",
				"-e", "HOME=/workspace",
				"-e", fmt.Sprintf("npm_config_registry=%s", registryURL),
				img.image,
				"sh", "-c",
				"npm install express && cat node_modules/express/package.json",
			}
			stdout, stderr, err := runDockerCommand(t, dockerArgs...)
			if err != nil {
				t.Fatalf("Docker command failed: %v\nStderr: %s", err, stderr)
			}
			// Parse package.json to verify version
			// Find the first standalone '{' which starts the JSON (after npm output like "added 50 packages...")
			jsonStart := strings.Index(stdout, "\n{")
			if jsonStart == -1 {
				jsonStart = strings.Index(stdout, "{")
			} else {
				jsonStart++ // skip the newline
			}
			if jsonStart == -1 {
				t.Fatalf("Could not find package.json in output: %s", stdout)
			}
			var pkgJSON struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal([]byte(stdout[jsonStart:]), &pkgJSON); err != nil {
				t.Fatalf("Failed to parse package.json: %v\nJSON attempted: %s", err, stdout[jsonStart:])
			}
			t.Logf("Installed express version %s with %s", pkgJSON.Version, img.description)
			if pkgJSON.Version != expectedVersion {
				t.Errorf("Expected version %s, got %s", expectedVersion, pkgJSON.Version)
			}
		})
	}
}

// TestTimewarpPyPIClientVersions tests that timewarp works correctly across different pip versions
func TestTimewarpPyPIClientVersions(t *testing.T) {
	checkDockerAvailable(t)
	port, cleanup := startTimewarpServer(t)
	t.Cleanup(cleanup)
	// Test same package at same timestamp across different pip versions
	// All should resolve to requests 2.22.0 (latest as of 2020-01-01)
	timestamp := "2020-01-01T00:00:00Z"
	expectedVersion := "2.22.0"
	images := []struct {
		image       string
		description string
	}{
		{"python:3.7-alpine", "pip ~20"},
		{"python:3.9-alpine", "pip ~22"},
		{"python:3.11-alpine", "pip ~24"},
	}
	for _, img := range images {
		t.Run(img.description, func(t *testing.T) {
			t.Parallel()
			indexURL := fmt.Sprintf("http://pypi:%s@127.0.0.1:%d/simple", timestamp, port)
			tempDir := t.TempDir()
			dockerArgs := []string{
				"run",
				"--rm",
				"--network=host",
				"--user", getUserID(),
				"-v", fmt.Sprintf("%s:/workspace", tempDir),
				"-w", "/workspace",
				"-e", "HOME=/workspace",
				"-e", fmt.Sprintf("PIP_INDEX_URL=%s", indexURL),
				img.image,
				"sh", "-c",
				"pip install --no-cache-dir requests && pip show requests",
			}
			stdout, stderr, err := runDockerCommand(t, dockerArgs...)
			if err != nil {
				t.Fatalf("Docker command failed: %v\nStderr: %s", err, stderr)
			}
			// Parse pip show output to get version
			var installedVersion string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Version:") {
					installedVersion = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
					break
				}
			}
			if installedVersion == "" {
				t.Fatalf("Could not find version in pip show output: %s", stdout)
			}
			t.Logf("Installed requests version %s with %s", installedVersion, img.description)
			if installedVersion != expectedVersion {
				t.Errorf("Expected version %s, got %s", expectedVersion, installedVersion)
			}
		})
	}
}

// TestTimewarpNPMLatestResolution tests that npm resolves the correct "latest" version at different timestamps
func TestTimewarpNPMLatestResolution(t *testing.T) {
	checkDockerAvailable(t)
	port, cleanup := startTimewarpServer(t)
	t.Cleanup(cleanup)
	// Test that "npm install express" (without version) gets correct latest at each timestamp
	tests := []struct {
		timestamp       string
		expectedVersion string
	}{
		// Express version history: https://www.npmjs.com/package/express?activeTab=versions
		{"2016-07-15T00:00:00Z", "4.14.0"},
		{"2018-01-01T00:00:00Z", "4.16.2"},
		{"2020-01-01T00:00:00Z", "4.17.1"},
	}
	for _, tt := range tests {
		t.Run(tt.timestamp, func(t *testing.T) {
			t.Parallel()
			registryURL := fmt.Sprintf("http://npm:%s@127.0.0.1:%d", tt.timestamp, port)
			tempDir := t.TempDir()
			dockerArgs := []string{
				"run",
				"--rm",
				"--network=host",
				"--user", getUserID(),
				"-v", fmt.Sprintf("%s:/workspace", tempDir),
				"-w", "/workspace",
				"-e", "HOME=/workspace",
				"-e", fmt.Sprintf("npm_config_registry=%s", registryURL),
				"node:18-alpine",
				"sh", "-c",
				"npm install express && cat node_modules/express/package.json",
			}
			stdout, stderr, err := runDockerCommand(t, dockerArgs...)
			if err != nil {
				t.Fatalf("Docker command failed: %v\nStderr: %s", err, stderr)
			}
			// Parse package.json to verify version
			// Find the first standalone '{' which starts the JSON (after npm output like "added 50 packages...")
			jsonStart := strings.Index(stdout, "\n{")
			if jsonStart == -1 {
				jsonStart = strings.Index(stdout, "{")
			} else {
				jsonStart++ // skip the newline
			}
			if jsonStart == -1 {
				t.Fatalf("Could not find package.json in output: %s", stdout)
			}
			var pkgJSON struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal([]byte(stdout[jsonStart:]), &pkgJSON); err != nil {
				t.Fatalf("Failed to parse package.json: %v\nJSON attempted: %s", err, stdout[jsonStart:])
			}
			t.Logf("At %s, express latest was %s", tt.timestamp, pkgJSON.Version)
			if pkgJSON.Version != tt.expectedVersion {
				t.Errorf("Expected version %s at %s, got %s", tt.expectedVersion, tt.timestamp, pkgJSON.Version)
			}
		})
	}
}

// TestTimewarpPyPILatestResolution tests that pip resolves the correct "latest" version at different timestamps
func TestTimewarpPyPILatestResolution(t *testing.T) {
	checkDockerAvailable(t)
	port, cleanup := startTimewarpServer(t)
	t.Cleanup(cleanup)
	// Test that "pip install requests" (without version) gets correct latest at each timestamp
	tests := []struct {
		timestamp       string
		expectedVersion string
	}{
		// Requests version history: https://pypi.org/project/requests/#history
		{"2018-01-01T00:00:00Z", "2.18.4"},
		{"2019-06-01T00:00:00Z", "2.22.0"},
		{"2020-01-01T00:00:00Z", "2.22.0"},
	}
	for _, tt := range tests {
		t.Run(tt.timestamp, func(t *testing.T) {
			t.Parallel()
			indexURL := fmt.Sprintf("http://pypi:%s@127.0.0.1:%d/simple", tt.timestamp, port)
			tempDir := t.TempDir()
			dockerArgs := []string{
				"run",
				"--rm",
				"--network=host",
				"--user", getUserID(),
				"-v", fmt.Sprintf("%s:/workspace", tempDir),
				"-w", "/workspace",
				"-e", "HOME=/workspace",
				"-e", fmt.Sprintf("PIP_INDEX_URL=%s", indexURL),
				"python:3.11-alpine",
				"sh", "-c",
				"pip install --no-cache-dir requests && pip show requests",
			}
			stdout, stderr, err := runDockerCommand(t, dockerArgs...)
			if err != nil {
				t.Fatalf("Docker command failed: %v\nStderr: %s", err, stderr)
			}
			// Parse pip show output to get version
			var installedVersion string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Version:") {
					installedVersion = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
					break
				}
			}
			if installedVersion == "" {
				t.Fatalf("Could not find version in pip show output: %s", stdout)
			}
			t.Logf("At %s, requests latest was %s", tt.timestamp, installedVersion)
			if installedVersion != tt.expectedVersion {
				t.Errorf("Expected version %s at %s, got %s", tt.expectedVersion, tt.timestamp, installedVersion)
			}
		})
	}
}
