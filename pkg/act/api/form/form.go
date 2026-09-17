// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"encoding/json"
	"net/url"
	"reflect"
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
	var opt fieldOptions
	parts := strings.Split(field.Tag.Get("form"), ",")
	if opt.name = parts[0]; opt.name == "" {
		opt.name = strings.ToLower(field.Name)
	}
	for _, val := range parts[1:] {
		if val == "required" {
			opt.required = true
		}
	}
	return opt
}

func Marshal(in any) (url.Values, error) {
	tvalue := reflect.Indirect(reflect.ValueOf(in))
	if tvalue.Kind() != reflect.Struct {
		return nil, ErrInvalidType
	}
	ttype := tvalue.Type()
	v := url.Values{}
	for i := range ttype.NumField() {
		field, value := ttype.Field(i), tvalue.Field(i)
		if !field.IsExported() {
			continue
		} else if field.Anonymous {
			return nil, errors.Wrapf(ErrUnsupportedField, "field '%s'", field.Name)
		}
		opt := options(field)
		if value.IsZero() {
			continue
		}
		switch field.Type.Kind() {
		case reflect.String:
			v.Set(opt.name, value.String())
		case reflect.Slice:
			if field.Type == stringSliceType {
				v[opt.name] = value.Interface().([]string)
				continue
			}
			fallthrough
		default:
			jsonv, err := json.Marshal(value.Interface())
			if err != nil {
				return nil, errors.Wrapf(err, "field '%s'", opt.name)
			}
			v.Set(opt.name, string(jsonv))
		}
	}
	return v, nil
}

func Unmarshal(v url.Values, out any) error {
	ptr := reflect.ValueOf(out)
	if ptr.Kind() != reflect.Pointer || ptr.IsNil() || ptr.Elem().Kind() != reflect.Struct {
		return ErrInvalidType
	}
	tvalue := ptr.Elem()
	ttype := tvalue.Type()
	for i := range ttype.NumField() {
		field, value := ttype.Field(i), tvalue.Field(i)
		if !field.IsExported() {
			continue
		} else if field.Anonymous {
			return errors.Wrapf(ErrUnsupportedField, "field '%s'", field.Name)
		}
		opt := options(field)
		vals := v[opt.name]
		// Scalars treat an empty value as absent since Marshal never emits their zero value.
		if len(vals) == 0 || (field.Type != stringSliceType && vals[0] == "") {
			if opt.required {
				return errors.Wrapf(ErrMissingRequired, "field '%s'", opt.name)
			}
			continue
		}
		switch field.Type.Kind() {
		case reflect.String:
			value.SetString(vals[0])
		case reflect.Slice:
			if field.Type == stringSliceType {
				value.Set(reflect.ValueOf(vals))
				continue
			}
			fallthrough
		default:
			if err := json.Unmarshal([]byte(vals[0]), value.Addr().Interface()); err != nil {
				return errors.Wrapf(err, "field '%s'", opt.name)
			}
		}
	}
	return nil
}
