package form

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
)

var (
	ErrInvalidType      = errors.New("invalid type")
	ErrUnsupportedField = errors.New("unsupported field")
	ErrMissingRequired  = errors.New("missing required field")
)

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
	tvalue := reflect.ValueOf(in)
	ttype := tvalue.Type()
	if ttype.Kind() == reflect.Pointer {
		tvalue = reflect.Indirect(tvalue)
		ttype = tvalue.Type()
	}
	if ttype.Kind() != reflect.Struct {
		return nil, ErrInvalidType
	}
	v := url.Values{}
	for i := range ttype.NumField() {
		field, value := ttype.Field(i), tvalue.Field(i)
		if !field.IsExported() {
			continue
		} else if field.Anonymous {
			return nil, ErrUnsupportedField
		}
		opt := options(field)
		if value.IsZero() {
			continue
		}
		switch field.Type.Kind() {
		case reflect.String:
			v.Set(opt.name, value.String())
		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.String {
				v[opt.name] = value.Interface().([]string)
				continue
			}
			fallthrough
		default:
			jsonv, err := json.Marshal(value.Interface())
			if err != nil {
				return nil, err
			}
			v.Set(opt.name, string(jsonv))
		}
	}
	return v, nil
}

func Unmarshal(v url.Values, out any) error {
	tvalue := reflect.ValueOf(out).Elem()
	ttype := tvalue.Type()
	if ttype.Kind() != reflect.Struct {
		return ErrInvalidType
	}
	for i := range ttype.NumField() {
		field, value := ttype.Field(i), tvalue.Field(i)
		if !field.IsExported() {
			continue
		} else if field.Anonymous {
			return ErrUnsupportedField
		}
		opt := options(field)
		urlval := v.Get(opt.name)
		if urlval == "" {
			if opt.required {
				return ErrMissingRequired
			}
			continue
		}
		switch field.Type.Kind() {
		case reflect.String:
			value.SetString(urlval)
		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.String {
				value.Set(reflect.ValueOf(v[opt.name]))
				continue
			}
			fallthrough
		default:
			err := json.Unmarshal([]byte(urlval), value.Addr().Interface())
			if err != nil {
				return err
			}
		}
	}
	return nil
}
