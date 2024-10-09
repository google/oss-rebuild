package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync"

	"github.com/elazarl/goproxy"
	"github.com/google/oss-rebuild/internal/proxy/handshake"
	"github.com/google/oss-rebuild/pkg/proxy/cert"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/proxy/policy"
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

type PolicyMode string

const (
	DisabledMode    PolicyMode = "disabled"
	EnforcementMode PolicyMode = "enforce"
)

func (m PolicyMode) IsValid() bool {
	switch m {
	case DisabledMode, EnforcementMode:
		return true
	default:
		return false
	}
}

// TransparentProxyService transparently proxies HTTP and HTTPS traffic.
type TransparentProxyService struct {
	Proxy  *goproxy.ProxyHttpServer
	Ca     *tls.Certificate
	Policy *policy.Policy
	Mode   PolicyMode

	mx            *sync.Mutex
	networkLog    *netlog.NetworkActivityLog
	adminShutdown func(context.Context) error
	httpShutdown  func(context.Context) error
	tlsShutdown   func(context.Context) error
}

// TransparentProxyServiceOpts defines the optional parameters for creating a TransparentProxyService.
type TransparentProxyServiceOpts struct {
	Policy      *policy.Policy
	SkipLogging bool
}

// NewTransparentProxyService creates a new TransparentProxyService.
func NewTransparentProxyService(p *goproxy.ProxyHttpServer, ca *tls.Certificate, mode PolicyMode, opts TransparentProxyServiceOpts) TransparentProxyService {
	m := new(sync.Mutex)
	if !mode.IsValid() {
		log.Fatalf("Invalid proxy mode specified: %v", mode)
	}
	if mode != DisabledMode && opts.Policy == nil {
		log.Fatalf("Invalid policy: %v", opts.Policy)
	}
	networkLog := &netlog.NetworkActivityLog{}
	if !opts.SkipLogging {
		networkLog = netlog.CaptureActivityLog(p, m)
	}
	return TransparentProxyService{
		Proxy:      p,
		Ca:         ca,
		Mode:       mode,
		Policy:     opts.Policy,
		mx:         m,
		networkLog: networkLog,
	}
}

func (t TransparentProxyService) Shutdown(ctx context.Context) error {
	if t.adminShutdown != nil {
		if err := t.adminShutdown(ctx); err != nil {
			return err
		}
	}
	if t.httpShutdown != nil {
		if err := t.httpShutdown(ctx); err != nil {
			return err
		}
	}
	if t.tlsShutdown != nil {
		if err := t.tlsShutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ProxyHTTP serves an endpoint that transparently redirects HTTP connections to the proxy server.
// This endpoint also explicitly (i.e. non-transparently) proxies HTTP and TLS connections.
func (t *TransparentProxyService) ProxyHTTP(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening for http connections - %v", err)
	}
	server := &http.Server{
		Addr:    addr,
		Handler: t.Proxy,
	}
	t.httpShutdown = func(ctx context.Context) error { return server.Shutdown(ctx) }
	if err := server.Serve(ln); err != nil {
		log.Println(err)
	}
}

// ProxyTLS serves an endpoint that transparently redirects TLS connections to the proxy server.
func (t *TransparentProxyService) ProxyTLS(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening for https connections - %v", err)
	}
	var inflightRequests sync.WaitGroup
	t.tlsShutdown = func(ctx context.Context) error {
		ch := make(chan struct{})
		errChan := make(chan error)
		go func() {
			if err := ln.Close(); err != nil {
				errChan <- err
				return
			}
			log.Println("Waiting for in-flight requests to complete...")
			inflightRequests.Wait()
			close(ch)
		}()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-errChan:
			return err
		case <-ch:
			return nil
		}
	}
	for {
		log.Println("Awaiting TLS connection")
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("Error accepting new connection - %v", err)
			continue
		}
		inflightRequests.Add(1)
		defer inflightRequests.Done()
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
			log.Printf("Connecting to %s", host)
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

func (t *TransparentProxyService) ServeAdmin(addr string) {
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
		if err := enc.Encode(t.networkLog); err != nil {
			log.Printf("Failed to marshal metadata: %v", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/policy", t.policyHandler)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	t.adminShutdown = func(ctx context.Context) error { return server.Shutdown(ctx) }
	if err := server.ListenAndServe(); err != nil {
		log.Print(err)
	}
}

// policyHandler handles requests to the /policy endpoint.
func (t *TransparentProxyService) policyHandler(w http.ResponseWriter, r *http.Request) {
	t.mx.Lock()
	defer t.mx.Unlock()
	switch r.Method {
	case http.MethodGet:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(t.Policy); err != nil {
			log.Printf("Failed to marshal metadata: %v", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
		}
	case http.MethodPut:
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Bad request received. Expected Content-Type: application/json", http.StatusBadRequest)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Internal Error", http.StatusInternalServerError)
		}

		var p policy.Policy
		err = json.Unmarshal(body, &p)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error unmarshaling request body: %v", err), http.StatusBadRequest)
		}
		t.Policy = &p
	default:
		log.Printf("Invalid method type received in request: %v", r.Method)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// Check that the requested url is allowed by the network policy.
func (proxy TransparentProxyService) ApplyNetworkPolicy(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if proxy.Mode == DisabledMode {
		return req, nil
	}
	return proxy.Policy.Apply(req, ctx)
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
