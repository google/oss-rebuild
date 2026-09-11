// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type ecosystem string

type target struct {
	Ecosystem ecosystem `json:"ecosystem"`
	Package   string    `json:"package"`
}

// wide holds one field of each kind the schema request types send.
type wide struct {
	Ecosystem ecosystem         `form:",required"`
	Names     []string          `form:"names"`
	Count     int               `form:""`
	Flag      bool              `form:""`
	Timeout   time.Duration     `form:""`
	Env       map[string]string `form:"env"`
	Target    target            `form:""`
	Hint      *target           `form:""`
	hidden    string
}

// full sets every exported field of wide.
var full = wide{
	Ecosystem: "npm",
	Names:     []string{"a", "b"},
	Count:     3,
	Flag:      true,
	Timeout:   time.Hour,
	Env:       map[string]string{"K": "V"},
	Target:    target{"pypi", "x"},
	Hint:      &target{"cratesio", "y"},
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   wide
		wire url.Values
	}{
		{
			name: "every kind",
			in:   full,
			wire: url.Values{
				"ecosystem": {"npm"},
				"names":     {"a", "b"},
				"count":     {"3"},
				"flag":      {"true"},
				"timeout":   {"3600000000000"},
				"env":       {`{"K":"V"}`},
				"target":    {`{"ecosystem":"pypi","package":"x"}`},
				"hint":      {`{"ecosystem":"cratesio","package":"y"}`},
			},
		},
		{
			name: "zero fields omitted",
			in:   wide{Ecosystem: "npm"},
			wire: url.Values{"ecosystem": {"npm"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if diff := cmp.Diff(tt.wire, got); diff != "" {
				t.Errorf("Marshal() mismatch (-want +got):\n%s", diff)
			}
			var out wide
			if err := Unmarshal(tt.wire, &out); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if diff := cmp.Diff(tt.in, out, cmp.AllowUnexported(wide{})); diff != "" {
				t.Errorf("Unmarshal() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Decoding into a populated struct overwrites only the keys present: an
// absent key, or an empty value for a scalar, leaves the field alone.
func TestUnmarshalPresence(t *testing.T) {
	got := full
	err := Unmarshal(url.Values{
		"ecosystem": {"pypi"},
		"count":     {""},
		"flag":      {"false"},
	}, &got)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	want := full
	want.Ecosystem, want.Flag = "pypi", false
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(wide{})); diff != "" {
		t.Errorf("Unmarshal() mismatch (-want +got):\n%s", diff)
	}
}

func TestErrors(t *testing.T) {
	type Target = target
	type embedded struct{ Target }
	tests := []struct {
		name    string
		call    func() error
		wantErr error // nil accepts any error
	}{
		{"marshal non-struct", func() error { _, err := Marshal("x"); return err }, ErrInvalidType},
		{"marshal embedded field", func() error { _, err := Marshal(embedded{}); return err }, ErrUnsupportedField},
		{"unmarshal non-struct", func() error { return Unmarshal(nil, new(string)) }, ErrInvalidType},
		{"unmarshal embedded field", func() error { return Unmarshal(nil, &embedded{}) }, ErrUnsupportedField},
		{"missing required", func() error { return Unmarshal(nil, &wide{}) }, ErrMissingRequired},
		{"bad json", func() error { return Unmarshal(url.Values{"ecosystem": {"npm"}, "count": {"z"}}, &wide{}) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestOptions(t *testing.T) {
	type testStruct struct {
		Field1 string `form:"custom_name,required"`
		Field2 string
		Field3 string `form:",required"`
	}
	tests := []struct {
		name  string
		field reflect.StructField
		want  fieldOptions
	}{
		{"custom name and required", reflect.TypeOf(testStruct{}).Field(0), fieldOptions{name: "custom_name", required: true}},
		{"default name", reflect.TypeOf(testStruct{}).Field(1), fieldOptions{name: "field2"}},
		{"default name required", reflect.TypeOf(testStruct{}).Field(2), fieldOptions{name: "field3", required: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, options(tt.field), cmp.AllowUnexported(fieldOptions{})); diff != "" {
				t.Errorf("options() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
