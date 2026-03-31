// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package proxy_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/proxy/netlog"
)

// checkDockerAvailable skips the test if Docker is not available.
func checkDockerAvailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker is not available, skipping integration test")
	}
}

var (
	proxyBinaryPath string
	buildOnce       sync.Once
	buildErr        error
)

// buildProxyBinary compiles cmd/proxy once per test run and returns the path.
// The binary is placed in os.TempDir() so it outlives any single test's t.TempDir().
func buildProxyBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		out, err := os.CreateTemp("", "proxy-integration-*")
		if err != nil {
			buildErr = fmt.Errorf("creating temp file for proxy binary: %v", err)
			return
		}
		out.Close()
		proxyBinaryPath = out.Name()
		cmd := exec.Command("go", "build", "-o", proxyBinaryPath, "github.com/google/oss-rebuild/cmd/proxy")
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
		if output, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building proxy binary: %v\n%s", err, output)
			return
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return proxyBinaryPath
}

// runDockerCommand runs a docker command and returns stdout, stderr, and error.
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
	}
	return stdout, stderr, err
}

// proxyTestEnv holds the Docker resources for a proxy integration test.
type proxyTestEnv struct {
	ProxyContainer string
	BuildContainer string
	Network        string
	ProxyIP        string
	AdminPort      string // host port mapped to proxy's admin endpoint
}

type proxyTestEnvOpts struct {
	BuildImage      string
	ExtraProxyFlags []string
	PolicyMode      string
	WithDockerProxy bool
	SkipCertInstall bool // if true, don't install proxy CA cert in build container
}

// setupProxyTestEnv creates the full Docker topology for a proxy integration test.
func setupProxyTestEnv(t *testing.T, bin string, opts proxyTestEnvOpts) *proxyTestEnv {
	t.Helper()

	if opts.BuildImage == "" {
		opts.BuildImage = "alpine:3.21"
	}
	if opts.PolicyMode == "" {
		opts.PolicyMode = "disabled"
	}

	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ToLower(name)
	network := "proxytest-" + name
	proxyName := "proxy-" + name
	buildName := "build-" + name

	// 1. Clean up any stale resources from previous failed runs, then create network.
	exec.Command("docker", "rm", "-f", proxyName, buildName).Run()
	exec.Command("docker", "network", "rm", network).Run()
	// The Docker API proxy creates named volumes (proxy-vol1, proxy-vol2, ...).
	// createFile skips writing if the file already exists, so stale volumes
	// from previous runs cause cert mismatches. Clean them up.
	cleanupProxyVolumes(t)
	if _, _, err := runDockerCommand(t, "network", "create", network); err != nil {
		t.Fatalf("Failed to create network: %v", err)
	}

	// 2. Start proxy container with binary copied in.
	proxyFlags := []string{
		"--http_addr=0.0.0.0:3128",
		"--tls_addr=0.0.0.0:3129",
		"--ctrl_addr=0.0.0.0:3127",
		"--policy_mode=" + opts.PolicyMode,
	}
	if opts.WithDockerProxy {
		proxyFlags = append(proxyFlags,
			"--docker_addr=0.0.0.0:3130",
			"--docker_socket=/var/run/docker.sock",
			// Use container:buildName so inner containers share the build
			// container's network namespace (inheriting iptables DNAT rules).
			"--docker_network=container:"+buildName,
		)
	}
	proxyFlags = append(proxyFlags, opts.ExtraProxyFlags...)

	runArgs := []string{
		"run", "-d",
		"--name=" + proxyName,
		"--network=" + network,
		"-p", "0:3127", // publish admin port
	}
	if opts.WithDockerProxy {
		runArgs = append(runArgs, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	}
	runArgs = append(runArgs, "alpine:3.21", "sleep", "infinity")

	if _, _, err := runDockerCommand(t, runArgs...); err != nil {
		t.Fatalf("Failed to start proxy container: %v", err)
	}

	// Copy binary into proxy container.
	if _, _, err := runDockerCommand(t, "cp", bin, proxyName+":/proxy"); err != nil {
		t.Fatalf("Failed to copy proxy binary: %v", err)
	}

	// Start proxy binary inside the container, logging to a file for debugging.
	proxyCmd := "/proxy " + strings.Join(proxyFlags, " ") + " > /proxy.log 2>&1"
	if _, _, err := runDockerCommand(t, "exec", "-d", proxyName, "sh", "-c", proxyCmd); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// Get published admin port.
	portOut, _, err := runDockerCommand(t, "port", proxyName, "3127")
	if err != nil {
		t.Fatalf("Failed to get admin port: %v", err)
	}
	// Output is like "0.0.0.0:32768\n"; extract the port.
	hostPort := strings.TrimSpace(portOut)
	parts := strings.Split(hostPort, "\n")
	// Use first line (may have IPv4 and IPv6 entries).
	adminAddr := strings.TrimSpace(parts[0])

	// Get proxy container IP on the test network.
	ipOut, _, err := runDockerCommand(t,
		"inspect", "-f",
		fmt.Sprintf("{{(index .NetworkSettings.Networks %q).IPAddress}}", network),
		proxyName,
	)
	if err != nil {
		t.Fatalf("Failed to get proxy IP: %v", err)
	}
	proxyIP := strings.TrimSpace(ipOut)

	// 3. Poll admin /cert endpoint until proxy is healthy.
	adminURL := fmt.Sprintf("http://%s/cert", adminAddr)
	var healthy bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(adminURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				healthy = true
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("Proxy admin endpoint not healthy at %s", adminURL)
	}

	// 4. Start build container.
	buildArgs := []string{
		"run", "-d",
		"--name=" + buildName,
		"--network=" + network,
		"--privileged",
		opts.BuildImage,
		"sleep", "infinity",
	}
	if _, _, err := runDockerCommand(t, buildArgs...); err != nil {
		t.Fatalf("Failed to start build container: %v", err)
	}

	// 5. Install packages needed in the build container (before iptables rules
	// redirect traffic through the proxy, which would break apk's HTTPS fetches).
	if _, _, err := runDockerCommand(t,
		"exec", buildName, "sh", "-c", "apk add --no-cache iptables",
	); err != nil {
		t.Fatalf("Failed to install packages in build container: %v", err)
	}

	// 6. Install proxy CA cert into the build container's trust store so HTTPS
	// through the proxy will be trusted once the DNAT rules are active.
	// Mirrors the production approach in pkg/build/gcb/planner.go: directly
	// append the cert to the system CA bundle.
	if !opts.SkipCertInstall {
		resp, err := http.Get(fmt.Sprintf("http://%s/cert", adminAddr))
		if err != nil {
			t.Fatalf("Failed to fetch proxy cert: %v", err)
		}
		certPEM, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("Failed to read proxy cert: %v", err)
		}
		t.Logf("Fetched proxy cert (%d bytes)", len(certPEM))
		cpCmd := exec.Command("docker", "exec", "-i", buildName, "sh", "-c",
			"tee /etc/ssl/certs/proxy.crt >> /etc/ssl/certs/ca-certificates.crt")
		// Ensure newline before PEM block so it doesn't merge with existing content.
		certData := string(certPEM)
		if !strings.HasPrefix(certData, "\n") {
			certData = "\n" + certData
		}
		cpCmd.Stdin = strings.NewReader(certData)
		if out, err := cpCmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to install cert in build container: %v\n%s", err, out)
		}
	}

	// 7. Set up iptables DNAT rules to redirect traffic through the proxy.
	iptablesScript := fmt.Sprintf(
		"iptables -t nat -A OUTPUT -p tcp --dport 3128 -j ACCEPT && "+
			"iptables -t nat -A OUTPUT -p tcp --dport 3129 -j ACCEPT && "+
			"iptables -t nat -A OUTPUT -p tcp --dport 3130 -j ACCEPT && "+
			"iptables -t nat -A OUTPUT -p tcp --dport 80 -j DNAT --to %s:3128 && "+
			"iptables -t nat -A OUTPUT -p tcp --dport 443 -j DNAT --to %s:3129",
		proxyIP, proxyIP)

	if _, _, err := runDockerCommand(t,
		"exec", "--privileged", buildName, "sh", "-c", iptablesScript,
	); err != nil {
		t.Fatalf("Failed to set iptables rules: %v", err)
	}

	// 8. Register cleanup.
	t.Cleanup(func() {
		runDockerCommand(t, "rm", "-f", proxyName, buildName)
		runDockerCommand(t, "network", "rm", network)
		cleanupProxyVolumes(t)
	})

	return &proxyTestEnv{
		ProxyContainer: proxyName,
		BuildContainer: buildName,
		Network:        network,
		ProxyIP:        proxyIP,
		AdminPort:      adminAddr,
	}
}

// runInBuild executes a command in the build container and returns stdout and stderr.
func (e *proxyTestEnv) runInBuild(t *testing.T, cmd string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, err := runDockerCommand(t,
		"exec", e.BuildContainer, "sh", "-c", cmd,
	)
	if err != nil {
		t.Fatalf("Command failed in build container: %v\nStdout: %s\nStderr: %s", err, stdout, stderr)
	}
	return stdout, stderr
}

// runInBuildExpectFail executes a command in the build container and expects it to fail.
func (e *proxyTestEnv) runInBuildExpectFail(t *testing.T, cmd string) (stdout, stderr string, err error) {
	t.Helper()
	stdout, stderr, err = runDockerCommand(t,
		"exec", e.BuildContainer, "sh", "-c", cmd,
	)
	if err == nil {
		t.Fatalf("Expected command to fail but it succeeded\nStdout: %s\nStderr: %s", stdout, stderr)
	}
	return stdout, stderr, err
}

// getProxyCert fetches the proxy CA certificate in PEM format from the admin endpoint.
func (e *proxyTestEnv) getProxyCert(t *testing.T) []byte {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s/cert", e.AdminPort))
	if err != nil {
		t.Fatalf("Failed to fetch proxy cert: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read proxy cert: %v", err)
	}
	return data
}

// getNetworkLog fetches the network activity log from the admin endpoint.
func (e *proxyTestEnv) getNetworkLog(t *testing.T) netlog.NetworkActivityLog {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s/summary", e.AdminPort))
	if err != nil {
		t.Fatalf("Failed to fetch network log: %v", err)
	}
	defer resp.Body.Close()
	var log netlog.NetworkActivityLog
	if err := json.NewDecoder(resp.Body).Decode(&log); err != nil {
		t.Fatalf("Failed to decode network log: %v", err)
	}
	return log
}

// setPolicy updates the proxy's network policy via the admin endpoint.
func (e *proxyTestEnv) setPolicy(t *testing.T, policyJSON string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("http://%s/policy", e.AdminPort),
		strings.NewReader(policyJSON),
	)
	if err != nil {
		t.Fatalf("Failed to create policy request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to set policy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Failed to set policy (status %d): %s", resp.StatusCode, body)
	}
}

// cleanupProxyVolumes removes Docker volumes created by the Docker API proxy.
func cleanupProxyVolumes(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(name, "proxy-vol") {
			exec.Command("docker", "volume", "rm", name).Run()
		}
	}
}

// --- Test Scenarios ---

func TestHTTPTransparentProxy(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{})

	stdout, _ := env.runInBuild(t, "wget -qO- http://example.com")
	if !strings.Contains(stdout, "Example Domain") {
		t.Errorf("Expected 'Example Domain' in output, got: %s", stdout)
	}

	log := env.getNetworkLog(t)
	found := false
	for _, r := range log.HTTPRequests {
		if r.Host == "example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected network log entry for example.com, got: %+v", log.HTTPRequests)
	}
}

func TestHTTPSTransparentProxy(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{})

	// Use a domain whose cert chain is trusted by Alpine's CA bundle.
	// (example.com's chain roots to a CA not in Alpine's default bundle.)
	stdout, _ := env.runInBuild(t, "wget -qO- https://www.google.com")
	if stdout == "" {
		t.Errorf("Expected non-empty response from https://www.google.com")
	}

	log := env.getNetworkLog(t)
	found := false
	for _, r := range log.HTTPRequests {
		if r.Host == "www.google.com" && r.Scheme == "https" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected HTTPS network log entry for www.google.com, got: %+v", log.HTTPRequests)
	}
}

func TestHTTPSWithoutCertFails(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{SkipCertInstall: true})

	// Without proxy CA cert, HTTPS should fail with TLS verification error.
	env.runInBuildExpectFail(t, "wget -qO- https://www.google.com")
}

func TestNetworkActivityLog(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{})

	// Make several requests.
	env.runInBuild(t, "wget -qO- http://example.com")
	env.runInBuild(t, "wget -qO- https://www.google.com > /dev/null")
	env.runInBuild(t, "wget -qO- http://example.org")

	log := env.getNetworkLog(t)

	type expected struct {
		host   string
		scheme string
	}
	wants := []expected{
		{"example.com", "http"},
		{"www.google.com", "https"},
		{"example.org", "http"},
	}
	for _, want := range wants {
		found := false
		for _, r := range log.HTTPRequests {
			if r.Host == want.host && r.Scheme == want.scheme {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected log entry for %s %s, got: %+v", want.scheme, want.host, log.HTTPRequests)
		}
	}
}

func TestTruststorePatching(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)

	// The Docker API proxy always creates /var/cache/proxy.crt in containers.
	// For distros with a pre-existing system truststore, it also appends the
	// cert there. Slim images (debian/ubuntu) lack ca-certificates so the
	// system truststore file doesn't exist; the proxy gracefully skips that.
	images := []struct {
		name                 string
		image                string
		systemTruststorePath string // empty = system truststore not expected
	}{
		{"alpine", "alpine:3.21", "/etc/ssl/cert.pem"},
		{"debian", "debian:bookworm-slim", ""},
		{"ubuntu", "ubuntu:24.04", ""},
	}

	for _, img := range images {
		t.Run(img.name, func(t *testing.T) {
			env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{
				WithDockerProxy: true,
			})

			// Install docker CLI in the build container so we can invoke docker commands.
			env.runInBuild(t, "apk add --no-cache docker-cli")

			// Fetch the proxy cert so we can verify it was injected.
			// Normalize line endings to handle \r\n from docker exec.
			proxyCert := strings.TrimSpace(strings.ReplaceAll(string(env.getProxyCert(t)), "\r\n", "\n"))

			// The proxy always creates /var/cache/proxy.crt in every container.
			dockerRunCmd := fmt.Sprintf(
				"DOCKER_HOST=tcp://%s:3130 docker run --rm %s cat /var/cache/proxy.crt",
				env.ProxyIP, img.image,
			)
			stdout, _ := env.runInBuild(t, dockerRunCmd)
			stdoutNorm := strings.TrimSpace(strings.ReplaceAll(stdout, "\r\n", "\n"))
			if !strings.Contains(stdoutNorm, proxyCert) {
				t.Errorf("Proxy cert not found in /var/cache/proxy.crt for %s\nExpected (%d bytes):\n%s\nGot (%d bytes):\n%s",
					img.name, len(proxyCert), proxyCert, len(stdoutNorm), stdoutNorm)
			}

			// For distros with a pre-existing system truststore, verify the
			// cert was also appended to the system CA bundle.
			if img.systemTruststorePath != "" {
				dockerRunCmd = fmt.Sprintf(
					"DOCKER_HOST=tcp://%s:3130 docker run --rm %s cat %s",
					env.ProxyIP, img.image, img.systemTruststorePath,
				)
				stdout, _ = env.runInBuild(t, dockerRunCmd)
				stdoutNorm = strings.TrimSpace(strings.ReplaceAll(stdout, "\r\n", "\n"))
				if !strings.Contains(stdoutNorm, proxyCert) {
					t.Errorf("Proxy cert not found in %s system truststore (%s)\nTruststore tail:\n%s",
						img.name, img.systemTruststorePath, stdoutNorm[max(0, len(stdoutNorm)-500):])
				}
			}
		})
	}
}

func TestNetworkPolicyEnforce(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{
		PolicyMode: "enforce",
	})

	// Set policy allowing only example.com.
	env.setPolicy(t, `{
		"Policy": {
			"anyOf": [
				{
					"ruleType": "URLMatchRule",
					"host": "example.com",
					"matchHostBy": "full",
					"path": "",
					"matchPathBy": "prefix"
				}
			]
		}
	}`)

	// Allowed request should succeed.
	stdout, _ := env.runInBuild(t, "wget -qO- http://example.com")
	if !strings.Contains(stdout, "Example Domain") {
		t.Errorf("Expected 'Example Domain' in output, got: %s", stdout)
	}

	// Blocked request should fail.
	env.runInBuildExpectFail(t, "wget -qO- http://httpbin.org/get")
}

func TestNetworkPolicyDynamic(t *testing.T) {
	checkDockerAvailable(t)
	bin := buildProxyBinary(t)
	env := setupProxyTestEnv(t, bin, proxyTestEnvOpts{
		PolicyMode: "enforce",
	})

	// Start with a permissive policy (suffix match on empty string = match any).
	env.setPolicy(t, `{
		"Policy": {
			"anyOf": [
				{
					"ruleType": "URLMatchRule",
					"host": "",
					"matchHostBy": "suffix",
					"path": "",
					"matchPathBy": "prefix"
				}
			]
		}
	}`)

	// Request should succeed with permissive policy.
	stdout, _ := env.runInBuild(t, "wget -qO- http://example.com")
	if !strings.Contains(stdout, "Example Domain") {
		t.Errorf("Expected 'Example Domain' in output, got: %s", stdout)
	}

	// Now switch to restrictive policy that only allows example.org.
	env.setPolicy(t, `{
		"Policy": {
			"anyOf": [
				{
					"ruleType": "URLMatchRule",
					"host": "example.org",
					"matchHostBy": "full",
					"path": "",
					"matchPathBy": "prefix"
				}
			]
		}
	}`)

	// Same request to example.com should now fail.
	env.runInBuildExpectFail(t, "wget -qO- http://example.com")
}
