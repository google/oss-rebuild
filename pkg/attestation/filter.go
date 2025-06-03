// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package attestation

import (
	"encoding/json"

	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/pkg/errors"
)

func reinterpretJSON[OutT, InT any](from InT) (*OutT, error) {
	data, err := json.Marshal(from)
	if err != nil {
		return nil, err
	}
	var result OutT
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type FilterOpt interface {
	matches(*in_toto.Statement) bool
}

func FilterFor[T any](b *Bundle, opts ...FilterOpt) ([]*T, error) {
	var matches []*T
	for _, envelope := range b.envelopes {
		match := true
		for _, opt := range opts {
			match = match && opt.matches(envelope.payload)
		}
		if match {
			converted, err := reinterpretJSON[T](envelope.payload)
			if err != nil {
				return nil, err
			}
			matches = append(matches, converted)
		}
	}
	return matches, nil
}

func FilterForOne[T any](b *Bundle, opts ...FilterOpt) (*T, error) {
	results, err := FilterFor[T](b, opts...)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, errors.Errorf("expected 1 result, got %d", len(results))
	}
	return results[0], nil
}

type predicateTypeFilter string

func (f predicateTypeFilter) matches(stmt *in_toto.Statement) bool {
	return stmt.PredicateType == string(f)
}

func WithPredicateType(predicateType string) predicateTypeFilter {
	return predicateTypeFilter(predicateType)
}

type buildTypeFilter string

func (f buildTypeFilter) matches(stmt *in_toto.Statement) bool {
	if predicateMap, ok := stmt.Predicate.(map[string]any); ok {
		if buildDef, ok := predicateMap["buildDefinition"].(map[string]any); ok {
			if buildType, ok := buildDef["buildType"].(string); ok {
				return buildType == string(f)
			}
		}
	}
	return false
}

func WithBuildType(buildType string) buildTypeFilter {
	return buildTypeFilter(buildType)
}

type genericFilter func(*in_toto.Statement) bool

func (f genericFilter) matches(stmt *in_toto.Statement) bool {
	cbk := (func(*in_toto.Statement) bool)(f)
	return cbk(stmt)
}

func With(fn func(*in_toto.Statement) bool) genericFilter {
	return genericFilter(fn)
}
