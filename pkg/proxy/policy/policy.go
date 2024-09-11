// Package policy defines the network policy that the proxy can choose to enforce.
package policy

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/elazarl/goproxy"
)

type Policy struct {
	Rules []Rule
}

func (p Policy) EnforcePolicy(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	for _, rule := range p.Rules {
		if rule.IsCompliant(req) {
			return req, nil
		}
	}
	log.Printf("Request to %s blocked by network policy", req.URL.String())
	errorMessage := fmt.Sprintf("Access to %s is blocked by the proxy's network policy", req.URL.String())
	return req, goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, errorMessage)
}

// Rule interface with method to check compliance of incoming http(s) requests.
type Rule interface {
	IsCompliant(req *http.Request) bool
}

type MatchingType string

const (
	FullPath   MatchingType = "fullpath"
	PathPrefix MatchingType = "pathprefix"
)

type URLMatchRule struct {
	Host string
	Path string
	Type MatchingType
}

func (rule URLMatchRule) IsCompliant(req *http.Request) bool {
	url := req.URL
	if url.Hostname() != rule.Host {
		return false
	}

	switch rule.Type {
	case PathPrefix:
		return strings.HasPrefix(url.Path, rule.Path)
	case FullPath:
		return url.Path == rule.Path
	default:
		return false
	}
}
