package netlog

import (
	"net"
	"net/http"
	"sync"

	"github.com/elazarl/goproxy"
)

type HTTPRequestLog struct {
	Method string
	Scheme string
	Host   string
	Path   string
}

type NetworkActivityLog struct {
	HTTPRequests []HTTPRequestLog
}

func CaptureActivityLog(t *goproxy.ProxyHttpServer, mx *sync.Mutex) *NetworkActivityLog {
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
