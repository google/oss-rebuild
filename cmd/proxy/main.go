// Package main defines an HTTP(S) proxy.
package main

import (
	"flag"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/proxy/cert"
	"github.com/google/oss-rebuild/pkg/proxy/docker"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/proxy/proxy"
)

var (
	verbose           = flag.Bool("verbose", true, "whether to output log events for each request")
	httpProxyAddr     = flag.String("http_addr", "localhost:3128", "address for HTTP proxy")
	tlsProxyAddr      = flag.String("tls_addr", "localhost:3129", "address for TLS proxy")
	ctrlAddr          = flag.String("ctrl_addr", "localhost:3127", "address for administrative endpoint")
	dockerAddr        = flag.String("docker_addr", "", "address for docker proxy endpoint")
	dockerSocket      = flag.String("docker_socket", "/var/run/docker.sock", "path to the docker socket")
	dockerNetwork     = flag.String("docker_network", "", "if provided, the docker network to use for all proxied containers")
	dockerEnvVars     = flag.String("docker_truststore_env_vars", "", "comma-separated env vars to populate with the proxy cert and patch into containers")
	dockerJavaEnvVar  = flag.Bool("docker_java_truststore", false, "whether to patch containers with Java proxy cert truststore file and env var")
	dockerProxySocket = flag.Bool("docker_recursive_proxy", false, "whether to patch containers with a unix domain socket which proxies docker requests from created containers")
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
	// Administrative endpoint.
	mx := new(sync.Mutex)
	proxyServer := proxy.TransparentProxyService{
		Proxy:      p,
		Ca:         ca,
		NetworkLog: netlog.CaptureActivityLog(p, mx),
	}
	go proxyServer.ServeMetadata(*ctrlAddr, mx)
	// Start proxy server endpoints.
	go proxyServer.ProxyTLS(*tlsProxyAddr)
	go proxyServer.ProxyHTTP(*httpProxyAddr)
	if len(*dockerAddr) > 0 {
		vars := strings.Split(*dockerEnvVars, ",")
		ctp, err := docker.NewContainerTruststorePatcher(*ca.Leaf, docker.ContainerTruststorePatcherOpts{
			EnvVars:         vars,
			JavaEnvVar:      *dockerJavaEnvVar,
			RecursiveProxy:  *dockerProxySocket,
			NetworkOverride: *dockerNetwork,
		})
		if err != nil {
			log.Fatalf("creating docker patcher: %v", err)
		}
		go ctp.Proxy(*dockerAddr, *dockerSocket)
	}

	// Sleep in the main thread.
	for {
		time.Sleep(time.Hour)
	}
}
