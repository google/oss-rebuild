// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package main defines an HTTP(S) proxy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/google/oss-rebuild/pkg/proxy/cert"
	"github.com/google/oss-rebuild/pkg/proxy/docker"
	"github.com/google/oss-rebuild/pkg/proxy/policy"
	"github.com/google/oss-rebuild/pkg/proxy/proxy"
)

var (
	verbose       = flag.Bool("verbose", true, "whether to output log events for each request")
	httpProxyAddr = flag.String("http_addr", "localhost:3128", "address for HTTP proxy")
	tlsProxyAddr  = flag.String("tls_addr", "localhost:3129", "address for TLS proxy")
	ctrlAddr      = flag.String("ctrl_addr", "localhost:3127", "address for administrative endpoint")
	dockerAddr    = flag.String("docker_addr", "", "address for docker proxy endpoint in the format host:port or tcp://host:port for tcp, or unix:///file for unix domain sockets.")
	// TODO: Add support for tcp sockets.
	dockerSocket               = flag.String("docker_socket", "/var/run/docker.sock", "path to the docker socket")
	dockerNetwork              = flag.String("docker_network", "", "if provided, the docker network to use for all proxied containers")
	dockerEnvVars              = flag.String("docker_env_vars", "", "comma-separated key-value pair env vars to patch into containers")
	dockerTruststoreEnvVars    = flag.String("docker_truststore_env_vars", "", "comma-separated env vars to populate with the proxy cert and patch into containers")
	dockerJavaTruststoreEnvVar = flag.Bool("docker_java_truststore", false, "whether to patch containers with Java proxy cert truststore file and env var")
	dockerBazelTruststore      = flag.Bool("docker_bazel_truststore", false, "whether to patch containers with global .bazelrc file pointing to the Java proxy cert truststore")
	dockerProxySocket          = flag.Bool("docker_recursive_proxy", false, "whether to patch containers with a unix domain socket which proxies docker requests from created containers")
	policyMode                 = flag.String("policy_mode", "disabled", "mode to run the proxy in. Options: disabled, enforce")
	policyFile                 = flag.String("policy_file", "", "path to a json file specifying the policy to apply to the proxy")
)

func main() {
	flag.Parse()

	// Configure ephemeral CA for proxy.
	ca := cert.GenerateCA()
	proxy.ConfigureGoproxyCA(ca)

	// Create and configure proxy server.
	if *verbose {
		log.Printf("Server starting up! - configured to listen on http interface %s and https interface %s", *httpProxyAddr, *tlsProxyAddr)
	}
	p := proxy.NewTransparentProxyServer(*verbose)
	policy.RegisterRule("URLMatchRule", func() policy.Rule { return &policy.URLMatchRule{} })
	var pl policy.Policy
	if *policyFile != "" {
		content, err := os.ReadFile(*policyFile)
		if err != nil {
			log.Fatalf("Error reading policy file: %v", err)
		}
		err = json.Unmarshal(content, &pl)
		if err != nil {
			log.Fatalf("Error unmarshaling policy file content: %v", err)
		}
	}
	proxyService := proxy.NewTransparentProxyService(p, ca, proxy.PolicyMode(*policyMode), proxy.TransparentProxyServiceOpts{
		Policy: &pl,
	})
	proxyService.Proxy.OnRequest().DoFunc(
		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			return proxyService.ApplyNetworkPolicy(req, ctx)
		})
	// Administrative endpoint.
	go proxyService.ServeAdmin(*ctrlAddr)
	// Start proxy server endpoints.
	go proxyService.ProxyTLS(*tlsProxyAddr)
	go proxyService.ProxyHTTP(*httpProxyAddr)
	if len(*dockerAddr) > 0 {
		var envVars, truststoreEnvVars []string
		if *dockerEnvVars != "" {
			envVars = strings.Split(*dockerEnvVars, ",")
		}
		if *dockerTruststoreEnvVars != "" {
			truststoreEnvVars = strings.Split(*dockerTruststoreEnvVars, ",")
		}
		ctp, err := docker.NewContainerTruststorePatcher(*ca.Leaf, docker.ContainerTruststorePatcherOpts{
			EnvVars:              envVars,
			TruststoreEnvVars:    truststoreEnvVars,
			JavaTruststoreEnvVar: *dockerJavaTruststoreEnvVar,
			BazelTruststore:      *dockerBazelTruststore,
			RecursiveProxy:       *dockerProxySocket,
			NetworkOverride:      *dockerNetwork,
		})
		if err != nil {
			log.Fatalf("creating docker patcher: %v", err)
		}
		go ctp.Proxy(*dockerAddr, *dockerSocket)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigChan

	log.Printf("Received signal: %v. Attempting graceful shutdown...", sig)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	if err := proxyService.Shutdown(ctx); err != nil {
		log.Fatalf("Error shutting down proxy: %v", err)
	} else {
		log.Printf("Successfully shutdown network proxy")
	}
}
