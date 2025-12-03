// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/pkg/errors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

type InitDeps[D act.Deps] func(context.Context) (D, error)
type HandlerFunc[I act.Input, O any, D act.Deps] func(context.Context, I, D) (*O, error)
type StubFunc[I act.Input, O any] func(context.Context, I) (*O, error)

// Type aliases for convenience
type NoDeps = act.NoDeps

var NoDepsInit = act.NoDepsInit

var (
	ErrNotOK       = errors.New("non-OK response")
	ErrExhausted   = status.New(codes.ResourceExhausted, "resource exhausted").Err()
	ErrUnavailable = status.New(codes.Unavailable, "service unavailable").Err()
)

func Stub[I act.Input, O any](client httpx.BasicClient, u *url.URL) StubFunc[I, O] {
	return func(ctx context.Context, i I) (*O, error) {
		values, err := form.Marshal(i)
		if err != nil {
			return nil, errors.Wrap(err, "serializing request")
		}
		if err := i.Validate(); err != nil {
			return nil, errors.Wrap(err, "serializing request")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(values.Encode()))
		if err != nil {
			return nil, errors.Wrap(err, "building http request")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.Wrap(err, "making http request")
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK: // Success: Skip error generation
		case http.StatusServiceUnavailable:
			if retryAfterStr := resp.Header.Get("Retry-After"); retryAfterStr != "" {
				if seconds, err := strconv.Atoi(retryAfterStr); err == nil && seconds > 0 {
					d := time.Duration(seconds) * time.Second
					return nil, AsStatus(codes.Unavailable, ErrUnavailable, RetryAfter(d))
				}
			}
			return nil, ErrUnavailable
		case http.StatusTooManyRequests:
			return nil, ErrExhausted
		default:
			b, _ := io.ReadAll(resp.Body)
			return nil, errors.Wrap(errors.Wrap(ErrNotOK, resp.Status), string(b))
		}
		var o O
		if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
			return nil, errors.Wrap(err, "decoding response")
		}
		return &o, nil
	}
}

func StubFromHandler[I act.Input, O any, D act.Deps](client httpx.BasicClient, u *url.URL, handler HandlerFunc[I, O, D]) StubFunc[I, O] {
	return Stub[I, O](client, u)
}

// AsStatus creates a gRPC status with the given code and error message.
// Optionally accepts status details to attach to the error.
func AsStatus(code codes.Code, err error, details ...proto.Message) error {
	s := status.New(code, err.Error())
	if len(details) == 0 {
		return s.Err()
	}
	p := s.Proto()
	for _, detail := range details {
		m, err := anypb.New(detail)
		if err != nil {
			log.Printf("Skipping detail which failed to convert: detail=%v,err=%v", detail, err)
			continue
		}
		p.Details = append(p.Details, m)
	}
	return status.FromProto(p).Err()
}

// RetryAfter is a convenience function for creating a detail proto for retry information.
// NOTE: For HTTP, should be limited to use with Unavailable and ResourceExhausted codes.
func RetryAfter(after time.Duration) proto.Message {
	return &errdetails.RetryInfo{
		RetryDelay: durationpb.New(after),
	}
}

var grpcToHTTP = map[codes.Code]int{
	codes.OK:                 http.StatusOK,
	codes.Canceled:           499, // Client Closed Request
	codes.Unknown:            http.StatusInternalServerError,
	codes.InvalidArgument:    http.StatusBadRequest,
	codes.DeadlineExceeded:   http.StatusGatewayTimeout,
	codes.NotFound:           http.StatusNotFound,
	codes.AlreadyExists:      http.StatusConflict,
	codes.PermissionDenied:   http.StatusForbidden,
	codes.ResourceExhausted:  http.StatusTooManyRequests,
	codes.FailedPrecondition: http.StatusBadRequest,
	codes.Aborted:            http.StatusConflict,
	codes.OutOfRange:         http.StatusBadRequest,
	codes.Unimplemented:      http.StatusNotImplemented,
	codes.Internal:           http.StatusInternalServerError,
	codes.Unavailable:        http.StatusServiceUnavailable,
	codes.DataLoss:           http.StatusInternalServerError,
	codes.Unauthenticated:    http.StatusUnauthorized,
}

func Handler[I act.Input, O any, D act.Deps](initDeps InitDeps[D], handler HandlerFunc[I, O, D]) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		r.ParseForm()
		var req I
		if err := form.Unmarshal(r.Form, &req); err != nil {
			log.Println(errors.Wrap(err, "parsing request"))
			http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		log.Printf("received request: %+v", req)
		if err := req.Validate(); err != nil {
			log.Println(errors.Wrap(err, "validating request"))
			http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		deps, err := initDeps(ctx)
		if err != nil {
			log.Println(errors.Wrap(err, "initializing dependencies"))
			http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		o, err := handler(ctx, req, deps)
		s := status.Convert(err)
		for _, detail := range s.Details() {
			switch d := detail.(type) {
			case *errdetails.RetryInfo:
				if d.RetryDelay != nil {
					if seconds := int(d.RetryDelay.Seconds); seconds > 0 {
						rw.Header().Set("Retry-After", strconv.Itoa(seconds))
					}
				}
			}
		}
		httpStatus, ok := grpcToHTTP[s.Code()]
		if !ok {
			log.Printf("unknown error code: %s\n", s.Code())
			httpStatus = http.StatusInternalServerError
		}
		if httpStatus != http.StatusOK {
			log.Println(s.Err())
			// NOTE: Use s.Message() as the body, instead of err.Error() This is
			// in case err was already a grpc status, then calling err.Error()
			// would be a verbose grpc error message.
			// TODO: Use a structured error type to avoid including unwanted
			// data. grpc status objects is one option. Another might be using
			// constant error messages with no dynamic information
			http.Error(rw, s.Message(), httpStatus)
			return
		}
		if o != nil {
			if err := json.NewEncoder(rw).Encode(o); err != nil {
				log.Println(errors.Wrap(err, "encoding response"))
				http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}
	}
}

type Translator[O act.Input] func(io.ReadCloser) (O, error)

// Translate applies a Translator on the Request.Body to populate the Request.URL params.
func Translate[O act.Input](t Translator[O], h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		m, err := t(r.Body)
		if err != nil {
			log.Println(errors.Wrap(err, "translating request"))
			http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		values, err := form.Marshal(m)
		if err != nil {
			log.Println(errors.Wrap(err, "marshalling request"))
			http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(""))
		r.PostForm = nil
		r.Form = nil
		r.URL.RawQuery = values.Encode()
		r.ParseForm()
		h(rw, r)
	}
}
