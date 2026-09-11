// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/pkg/errors"
)

var (
	ErrInvalidType      = errors.New("invalid type")
	ErrUnsupportedField = errors.New("unsupported field")
	ErrMissingRequired  = errors.New("missing required field")
)

var stringSliceType = reflect.TypeFor[[]string]()

type fieldOptions struct {
	name     string
	required bool
}

func options(field reflect.StructField) fieldOptions {
	name, rest, _ := strings.Cut(field.Tag.Get("form"), ",")
	if name == "" {
		name = strings.ToLower(field.Name)
	}
	return fieldOptions{name: name, required: slices.Contains(strings.Split(rest, ","), "required")}
}

// walk applies fn to each exported field of the struct s.
func walk(s reflect.Value, fn func(fieldOptions, reflect.Value) error) error {
	t := s.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		} else if field.Anonymous {
			return errors.Wrapf(ErrUnsupportedField, "field '%s'", field.Name)
		}
		if err := fn(options(field), s.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

func Marshal(in any) (url.Values, error) {
	s := reflect.Indirect(reflect.ValueOf(in))
	if s.Kind() != reflect.Struct {
		return nil, ErrInvalidType
	}
	v := url.Values{}
	err := walk(s, func(opt fieldOptions, value reflect.Value) error {
		switch {
		case value.IsZero():
		case value.Kind() == reflect.String:
			v.Set(opt.name, value.String())
		case value.Type() == stringSliceType:
			v[opt.name] = value.Interface().([]string)
		default:
			b, err := json.Marshal(value.Interface())
			if err != nil {
				return errors.Wrapf(err, "field '%s'", opt.name)
			}
			v.Set(opt.name, string(b))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func Unmarshal(v url.Values, out any) error {
	ptr := reflect.ValueOf(out)
	if ptr.Kind() != reflect.Pointer || ptr.IsNil() || ptr.Elem().Kind() != reflect.Struct {
		return ErrInvalidType
	}
	return walk(ptr.Elem(), func(opt fieldOptions, value reflect.Value) error {
		vals := v[opt.name]
		switch {
		case len(vals) == 0 || (value.Type() != stringSliceType && vals[0] == ""):
			if opt.required {
				return errors.Wrapf(ErrMissingRequired, "field '%s'", opt.name)
			}
		case value.Kind() == reflect.String:
			value.SetString(vals[0])
		case value.Type() == stringSliceType:
			value.Set(reflect.ValueOf(vals))
		default:
			if err := json.Unmarshal([]byte(vals[0]), value.Addr().Interface()); err != nil {
				return errors.Wrapf(err, "field '%s'", opt.name)
			}
		}
		return nil
	})
}
