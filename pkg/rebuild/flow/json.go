// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"encoding/json"

	"github.com/pkg/errors"
)

// ToJSON converts any data to a JSON string and returns any encoding errors.
func ToJSON(data any) (string, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return "", errors.Wrap(err, "marshaling to JSON")
	}
	return string(bytes), nil
}

// MustToJSON converts any data to a JSON string, panicking if an error occurs.
func MustToJSON(data any) string {
	if s, err := ToJSON(data); err != nil {
		panic(err)
	} else {
		return s
	}
}

// FromJSON converts a JSON string into a value and returns any decoding errors.
func FromJSON(s string) (any, error) {
	var result any
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, errors.Wrap(err, "unmarshaling from JSON")
	}
	return result, nil
}

// MustFromJSON converts a JSON string into a value, panicking if an error occurs.
func MustFromJSON(s string) any {
	if data, err := FromJSON(s); err != nil {
		panic(err)
	} else {
		return data
	}
}
