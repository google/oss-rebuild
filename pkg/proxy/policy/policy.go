// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package policy defines the network policy that the proxy can choose to enforce.
package policy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/elazarl/goproxy"
)

var ruleRegistry = map[string]func() Rule{}

// RegisterRule adds a rule to the rule registry.
// The rule registry contains implementations of the Rule interface recognized
// by the proxy. Updates to the policy will try to unmarshal json structs into the
// registered rules. Expects the rule name and a constructor returning the Rule type.
func RegisterRule(rulename string, constructor func() Rule) {
	ruleRegistry[rulename] = constructor
}

// Policy contains a list of Rules that will be applied to the request.
type Policy struct {
	// AnyOf expects incoming requests to satisfy one of these Rules.
	AnyOf []Rule `json:"anyOf"`

	// AllOf expects incoming requests to satisfy all of these Rules.
	AllOf []Rule `json:"allOf"`
}

// UnmarshalJSON implements the json.Unmarshaler interface for the Policy class.
// Expects rule_type to specify an existing rule in the rule registry.
func (p *Policy) UnmarshalJSON(data []byte) error {
	var policywrapper struct {
		Policy struct {
			AnyOf []json.RawMessage
			AllOf []json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &policywrapper); err != nil {
		return err
	}
	for _, r := range policywrapper.Policy.AnyOf {
		if rule, err := newRuleFromJson(r); err != nil {
			return err
		} else {
			p.AnyOf = append(p.AnyOf, rule)
		}
	}
	for _, r := range policywrapper.Policy.AllOf {
		if rule, err := newRuleFromJson(r); err != nil {
			return err
		} else {
			p.AllOf = append(p.AllOf, rule)
		}
	}
	return nil
}

func newRuleFromJson(rule json.RawMessage) (Rule, error) {
	var tmpmap map[string]any
	if err := json.Unmarshal(rule, &tmpmap); err != nil {
		return nil, err
	}
	if _, ok := tmpmap["ruleType"]; !ok {
		return nil, fmt.Errorf("rule_type not specified in Rule: %v", string(rule))
	}
	ruleType := tmpmap["ruleType"].(string)
	if constructor, ok := ruleRegistry[ruleType]; !ok {
		return nil, fmt.Errorf("unexpected rule_type specified: '%s'", ruleType)
	} else {
		newRule := constructor()
		if err := json.Unmarshal(rule, &newRule); err != nil {
			return nil, err
		} else {
			return newRule, nil
		}
	}
}

// Apply enforces the policy on the request. Returns http.StatusForbidden if the
// request does not satisfy the policy rules.
func (p Policy) Apply(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	for _, rule := range p.AllOf {
		if !rule.Allows(req) {
			return blockedResponse(req)
		}
	}
	if len(p.AllOf) != 0 && len(p.AnyOf) == 0 {
		return req, nil
	}
	for _, rule := range p.AnyOf {
		if rule.Allows(req) {
			return req, nil
		}
	}
	return blockedResponse(req)
}

func blockedResponse(req *http.Request) (*http.Request, *http.Response) {
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
	FullMatch   MatchingType = "full"
	PrefixMatch MatchingType = "prefix"
	SuffixMatch MatchingType = "suffix"
)

// Implements the Rule interface. Matches the request URL based on the MatchingType.
type URLMatchRule struct {
	Host      string       `json:"host"`
	HostMatch MatchingType `json:"matchHostBy"`
	Path      string       `json:"path"`
	PathMatch MatchingType `json:"matchPathBy"`
}

// Allows validates the rule against the URL in req.
//
// Matching host by suffix assumes domain parts rather than plain string suffix.
// That is, a domain suffix always starts with the dot delimiter.
// E.g. notgoogle.com does not match google.com, but is.google.com does.
// The empty string matches any domain.
func (rule URLMatchRule) Allows(req *http.Request) bool {
	url := req.URL
	switch rule.HostMatch {
	case SuffixMatch:
		// Special case: match any.
		if rule.Host == "" {
			return true
		}

		// Check for an exact match first (see below).
		if url.Hostname() == rule.Host {
			return true
		}

		// Avoid matching partial domain names and only match full domain parts.
		// That is, notgoogle.com must not match google.com, but is.google.com matches google.com.
		host := rule.Host
		if !strings.HasPrefix(host, ".") {
			host = "." + host
		}
		if !strings.HasSuffix(url.Hostname(), host) {
			return false
		}
	case FullMatch:
		if url.Hostname() != rule.Host {
			return false
		}
	default:
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
