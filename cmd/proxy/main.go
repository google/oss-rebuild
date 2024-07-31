// Package main defines an HTTP(S) proxy.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/google/oss-rebuild/internal/proxy/certfmt"
	"github.com/google/oss-rebuild/internal/proxy/docker"
	"github.com/google/oss-rebuild/internal/proxy/handshake"
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

// TLS port to which proxied TLS traffic should be redirected.
// Used to enable customization during testing.
var tlsPort = "443"

func generateCA() *tls.Certificate {
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2394),
		Subject: pkix.Name{
			CommonName: "OSS Rebuild Proxy",
			Locality:   []string{"New York City"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	// TODO: Switch to ed25519.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("failed to generate key: %v", err)
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		log.Fatalf("failed to create CA: %v", err)
	}
	ca := new(tls.Certificate)
	ca.Certificate = append(ca.Certificate, caBytes)
	ca.PrivateKey = priv
	if ca.Leaf, err = x509.ParseCertificate(caBytes); err != nil {
		log.Fatalf("failed to parse CA leaf: %v", err)
	}
	return ca
}

// ConfigureGoproxyCA sets the global intermediate CA used by goproxy.
func ConfigureGoproxyCA(ca *tls.Certificate) {
	// TODO: Refactor TLSConfigFromCA to support ed25519.
	goproxy.OkConnect = &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.MitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.HTTPMitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectHTTPMitm, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.RejectConnect = &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: goproxy.TLSConfigFromCA(ca)}
}

// TransparentProxyServer transparently proxies HTTP and HTTPS traffic.
type TransparentProxyServer struct {
	*goproxy.ProxyHttpServer
}

// NewTransparentProxyServer constructs a TransparentProxyServer.
func NewTransparentProxyServer(verbose bool) *TransparentProxyServer {
	t := &TransparentProxyServer{goproxy.NewProxyHttpServer()}
	t.Verbose = verbose
	// Ignore pre-existing http(s) proxy env vars.
	t.ConnectDial = nil
	t.Tr = &http.Transport{
		// Disable default insecure connection by proxy.
		// TODO: This behavior may break clients who need to install and
		// use custom TLS certificates. Roots could be customized on the fly to
		// support this set of use-cases.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	t.NonproxyHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		log.Printf("Nonproxy handler: %s", req.Host)
		if req.Host == "" {
			fmt.Fprintln(w, "proxy: required Host header not populated. HTTP 1.0 request?")
			return
		}
		req.URL.Scheme = "http"
		req.URL.Host = req.Host
		t.ServeHTTP(w, req)
	})
	// NOTE: This could be included in upstream.
	var alwaysMitmHTTP goproxy.FuncHttpsHandler = func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		return goproxy.HTTPMitmConnect, host
	}
	t.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile("^[^:]*(:80)?$"))).
		HandleConnect(alwaysMitmHTTP)
	t.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile("^.*:" + tlsPort + "$"))).
		HandleConnect(goproxy.AlwaysMitm)
	return t
}

// ProxyHTTP serves an endpoint that transparently redirects HTTP connections to the proxy server.
// This endpoint also explicitly (i.e. non-transparently) proxies HTTP and TLS connections.
func (t TransparentProxyServer) ProxyHTTP(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening for http connections - %v", err)
	}
	if err := http.Serve(ln, &t); err != nil {
		log.Fatalln(err)
	}
}

// ProxyTLS serves an endpoint that transparently redirects TLS connections to the proxy server.
func (t TransparentProxyServer) ProxyTLS(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening for https connections - %v", err)
	}
	for {
		log.Println("Awaiting TLS connection")
		c, err := ln.Accept()
		if err != nil {
			log.Printf("Error accepting new connection - %v", err)
			continue
		}
		go func(c net.Conn) {
			log.Println("Connection from ", c.RemoteAddr())
			conn, hello, err := handshake.PeekClientHello(c)
			if err != nil {
				log.Printf("Error accepting new connection - %v", err)
				return
			}
			host := hello.ServerName
			if host == "" {
				log.Printf("Cannot support non-SNI enabled clients")
				c.Close()
				return
			}
			log.Printf("Got connection from: %s", host)
			connectReq := &http.Request{
				Method: "CONNECT",
				URL: &url.URL{
					Opaque: host,
					Host:   net.JoinHostPort(host, tlsPort),
				},
				Host:       net.JoinHostPort(host, tlsPort),
				Header:     make(http.Header),
				RemoteAddr: c.RemoteAddr().String(),
			}
			resp := eatConnectResponseWriter{conn}
			t.ServeHTTP(resp, connectReq)
		}(c)
	}
}

type HTTPRequestLog struct {
	Method string
	Scheme string
	Host   string
	Path   string
}

type NetworkActivityLog struct {
	HTTPRequests []HTTPRequestLog
}

func captureActivityLog(t *TransparentProxyServer, mx *sync.Mutex) *NetworkActivityLog {
	httpReqs := make(chan HTTPRequestLog, 10)
	t.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Schema-less requests will be raw HTTP requests with relative URLs.
		if req.URL.Scheme == "" {
			req.URL.Scheme = "http"
			req.URL.Host = req.Host
		}
		// Only retain non-standard port numbers from Host.
		host, port, err := net.SplitHostPort(req.URL.Host)
		if err != nil || !((port == "80" && req.URL.Scheme == "http") || (port == "443" && req.URL.Scheme == "https")) {
			host = req.URL.Host
		}
		httpReqs <- HTTPRequestLog{
			Method: req.Method,
			Scheme: req.URL.Scheme,
			Host:   host,
			Path:   req.URL.Path,
		}
		return req, nil
	})
	netlog := new(NetworkActivityLog)
	go func() {
		for {
			select {
			case httpReq := <-httpReqs:
				mx.Lock()
				netlog.HTTPRequests = append(netlog.HTTPRequests, httpReq)
				mx.Unlock()
			}
		}
	}()
	return netlog
}

func serveMetadata(addr string, ca *tls.Certificate, m *NetworkActivityLog, mx *sync.Mutex) {
	pemBytes := certfmt.ToPEM(ca.Leaf)
	jksBytes, err := certfmt.ToJKS(ca.Leaf)
	if err != nil {
		log.Fatalf("failed to generate Java KeyStore cert: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cert", func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		switch r.URL.Query().Get("format") {
		case "jks":
			b = jksBytes
		default:
			b = pemBytes
		}
		if _, err := w.Write(b); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/summary", func(w http.ResponseWriter, r *http.Request) {
		mx.Lock()
		defer mx.Unlock()
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			log.Printf("Failed to marshal metadata: %v", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
		}
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// eatConnectResponseWriter drops the goproxy response to the HTTP CONNECT tunnel creation.
type eatConnectResponseWriter struct {
	net.Conn
}

func (tc eatConnectResponseWriter) Header() http.Header {
	panic("unexpected Header() call")
}

func (tc eatConnectResponseWriter) Write(buf []byte) (int, error) {
	if bytes.Equal(buf, []byte("HTTP/1.0 200 OK\r\n\r\n")) {
		return len(buf), nil // ignore the HTTP OK response Write() from the CONNECT request
	}
	return tc.Conn.Write(buf)
}

func (tc eatConnectResponseWriter) WriteHeader(code int) {
	panic("unexpected WriteHeader() call")
}

func (tc eatConnectResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return tc, bufio.NewReadWriter(bufio.NewReader(tc), bufio.NewWriter(tc)), nil
}

func main() {
	flag.Parse()

	// Configure ephemeral CA for proxy.
	ca := generateCA()
	ConfigureGoproxyCA(ca)

	// Create and configure proxy server.
	if *verbose {
		log.Printf("Server starting up! - configured to listen on http interface %s and https interface %s", *httpProxyAddr, *tlsProxyAddr)
	}
	p := NewTransparentProxyServer(*verbose)
	// Administrative endpoint.
	mx := new(sync.Mutex)
	l := captureActivityLog(p, mx)
	go serveMetadata(*ctrlAddr, ca, l, mx)
	// Start proxy server endpoints.
	go p.ProxyTLS(*tlsProxyAddr)
	go p.ProxyHTTP(*httpProxyAddr)
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
