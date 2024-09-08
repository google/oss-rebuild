package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/elazarl/goproxy"
	"github.com/google/oss-rebuild/internal/proxy/handshake"
	"github.com/google/oss-rebuild/pkg/proxy/cert"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
)

// TLS port to which proxied TLS traffic should be redirected.
// Used to enable customization during testing.
var tlsPort = "443"

// ConfigureGoproxyCA sets the global intermediate CA used by goproxy.
func ConfigureGoproxyCA(ca *tls.Certificate) {
	// TODO: Refactor TLSConfigFromCA to support ed25519.
	goproxy.OkConnect = &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.MitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.HTTPMitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectHTTPMitm, TLSConfig: goproxy.TLSConfigFromCA(ca)}
	goproxy.RejectConnect = &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: goproxy.TLSConfigFromCA(ca)}
}

// NewTransparentProxyServer constructs a ProxyHttpServer.
func NewTransparentProxyServer(verbose bool) *goproxy.ProxyHttpServer {
	t := goproxy.NewProxyHttpServer()
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

type ProxyMode int

const (
	MonitorMode ProxyMode = iota
	EnforcementMode
	UnkownMode
)

func StringToProxyMode(mode string) ProxyMode {
	switch mode {
	case "monitor":
		return MonitorMode
	case "enforce":
		return EnforcementMode
	default:
		return UnkownMode
	}
}

// TransparentProxyService transparently proxies HTTP and HTTPS traffic.
type TransparentProxyService struct {
	Proxy      *goproxy.ProxyHttpServer
	Ca         *tls.Certificate
	NetworkLog *netlog.NetworkActivityLog
	Policy     *NetworkPolicy
	Mode       ProxyMode

	mx *sync.Mutex
}

// NewTransparentProxyService creates a new TransparentProxyService.
func NewTransparentProxyService(p *goproxy.ProxyHttpServer, ca *tls.Certificate, mode string) TransparentProxyService {
	m := new(sync.Mutex)
	pm := StringToProxyMode(mode)
	if pm == UnkownMode {
		log.Fatalf("Invalid proxy mode specified: %v", mode)
	}
	return TransparentProxyService{
		Proxy:      p,
		Ca:         ca,
		NetworkLog: netlog.CaptureActivityLog(p, m),
		Mode:       pm,
		mx:         m,
	}
}

// ProxyHTTP serves an endpoint that transparently redirects HTTP connections to the proxy server.
// This endpoint also explicitly (i.e. non-transparently) proxies HTTP and TLS connections.
func (t TransparentProxyService) ProxyHTTP(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening for http connections - %v", err)
	}
	if err := http.Serve(ln, t.Proxy); err != nil {
		log.Fatalln(err)
	}
}

// ProxyTLS serves an endpoint that transparently redirects TLS connections to the proxy server.
func (t TransparentProxyService) ProxyTLS(addr string) {
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
			t.Proxy.ServeHTTP(resp, connectReq)
		}(c)
	}
}

func (t *TransparentProxyService) ServeMetadata(addr string) {
	pemBytes := cert.ToPEM(t.Ca.Leaf)
	jksBytes, err := cert.ToJKS(t.Ca.Leaf)
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
		t.mx.Lock()
		defer t.mx.Unlock()
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(t.NetworkLog); err != nil {
			log.Printf("Failed to marshal metadata: %v", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
		}
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// Check that the requested url is allowed by the network policy.
func (proxy TransparentProxyService) CheckNetworkPolicy(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if proxy.Mode != EnforcementMode {
		return req, nil
	}
	url := req.URL
	if proxy.Policy != nil {
		for _, rule := range proxy.Policy.Rules {
			if url.Hostname() != rule.Host {
				continue
			}
			if urlSatisfiesRule(url, rule) {
				return req, nil
			}
		}
	}
	log.Printf("Request to %s blocked by network policy", url.String())
	errorMessage := fmt.Sprintf("Access to %s is blocked by the proxy's network policy", url.String())
	return req, goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, errorMessage)
}

type MatchingStrategy int

const (
	PathPrefix MatchingStrategy = iota
	FullPath
)

type NetworkPolicy struct {
	Rules []NetworkPolicyRule
}

type NetworkPolicyRule struct {
	Host     string
	Path     string
	Strategy MatchingStrategy
}

func urlSatisfiesRule(url *url.URL, rule NetworkPolicyRule) bool {
	switch rule.Strategy {
	case PathPrefix:
		return strings.HasPrefix(url.Path, rule.Path)
	case FullPath:
		return url.Path == rule.Path
	default:
		return false
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
