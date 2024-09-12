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
	AnyOf []Rule
}

func (p Policy) Apply(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if p.AnyOf == nil || len(p.AnyOf) == 0 {
		return req, nil
	}
	for _, rule := range p.AnyOf {
		if rule.Allows(req) {
			return req, nil
		}
	}
	log.Printf("Request to %s blocked by network policy", req.URL.String())
	errorMessage := fmt.Sprintf("Access to %s is blocked by the proxy's network policy", req.URL.String())
	return req, goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, errorMessage)
}

// Rule interface with method to check compliance of incoming http(s) requests.
type Rule interface {
	Allows(req *http.Request) bool
}

type MatchingType string

const (
	FullMatch   MatchingType = "full_path"
	PrefixMatch MatchingType = "prefix"
)

type URLMatchRule struct {
	Host      string
	Path      string
	PathMatch MatchingType
}

func (rule URLMatchRule) Allows(req *http.Request) bool {
	url := req.URL
	if url.Hostname() != rule.Host {
		return false
	}

	switch rule.PathMatch {
	case PrefixMatch:
		return strings.HasPrefix(url.Path, rule.Path)
	case FullMatch:
		return url.Path == rule.Path
	default:
		return false
	}
}
