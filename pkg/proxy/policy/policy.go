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
}

// UnmarshalJSON implements the json.Unmarshaler interface for the Policy class.
// Expects rule_type to specify an existing rule in the rule registry.
func (p *Policy) UnmarshalJSON(data []byte) error {
	var policywrapper struct {
		Policy struct {
			AnyOf []json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &policywrapper); err != nil {
		return err
	}
	err := loadRules(policywrapper.Policy.AnyOf, &p.AnyOf)
	return err
}

func loadRules(rules []json.RawMessage, ruleSet *[]Rule) error {
	for _, rule := range rules {
		var tmpmap map[string]any
		if err := json.Unmarshal(rule, &tmpmap); err != nil {
			return err
		}

		if _, ok := tmpmap["ruleType"]; !ok {
			return fmt.Errorf("rule_type not specified in Rule: %v", string(rule))
		}

		ruleType := tmpmap["ruleType"].(string)
		if registeredrule, ok := ruleRegistry[ruleType]; !ok {
			return fmt.Errorf("unexpected rule_type specified: '%s'", ruleType)
		} else {
			newRule := registeredrule()
			if err := json.Unmarshal(rule, &newRule); err == nil {
				*ruleSet = append(*ruleSet, newRule)
			}
		}
	}
	return nil
}

// Apply enforces the policy on the request. Returns http.StatusForbidden if the
// request does not satisfy the policy rules.
func (p Policy) Apply(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
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
	FullMatch   MatchingType = "full"
	PrefixMatch MatchingType = "prefix"
)

// Implements the Rule interface. Matches the request URL based on the MatchingType.
type URLMatchRule struct {
	Host      string       `json:"host"`
	Path      string       `json:"path"`
	PathMatch MatchingType `json:"matchPathBy"`
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
