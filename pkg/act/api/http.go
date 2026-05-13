// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"html/template"
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
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
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
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			if st, ok := readStatusFromBody(body); ok && st.Code() != codes.OK {
				return nil, st.Err()
			}
			// Fallback for servers that don't emit a Status body (or non-parseable
			// body, e.g. load-balancer error pages). Synthesize from HTTP code.
			switch resp.StatusCode {
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
				return nil, errors.Wrap(errors.Wrap(ErrNotOK, resp.Status), string(body))
			}
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

// readStatusFromBody decodes a gRPC Status proto from an HTTP error response
// body. Returns (status, true) on successful parse.
func readStatusFromBody(b []byte) (*status.Status, bool) {
	if len(b) == 0 {
		return nil, false
	}
	sp := &spb.Status{}
	if err := protojson.Unmarshal(b, sp); err != nil {
		return nil, false
	}
	return status.FromProto(sp), true
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

func jsonResponse[O any](rw http.ResponseWriter, o *O) error {
	if o != nil {
		return json.NewEncoder(rw).Encode(o)
	}
	return nil
}

func templateResponse[O any](tmpl *template.Template) func(http.ResponseWriter, *O) error {
	return func(rw http.ResponseWriter, o *O) error {
		if o != nil {
			return tmpl.Execute(rw, o)
		}
		return nil
	}
}

func WithTimeout[I act.Input, O any, D act.Deps](timeout time.Duration, handler HandlerFunc[I, O, D]) HandlerFunc[I, O, D] {
	return func(ctx context.Context, req I, deps D) (*O, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handler(ctx, req, deps)
	}
}

func Handler[I act.Input, O any, D act.Deps](initDeps InitDeps[D], handler HandlerFunc[I, O, D]) http.HandlerFunc {
	return handleUsingResponder(initDeps, handler, jsonResponse)
}

func HTMLHandler[I act.Input, O any, D act.Deps](initDeps InitDeps[D], handler HandlerFunc[I, O, D], tmpl *template.Template) http.HandlerFunc {
	return handleUsingResponder(initDeps, handler, templateResponse[O](tmpl))
}

func handleUsingResponder[I act.Input, O any, D act.Deps](initDeps InitDeps[D], handler HandlerFunc[I, O, D], responder func(http.ResponseWriter, *O) error) http.HandlerFunc {
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
			// Emit the gRPC Status proto as protojson so stubs can recover the
			// full code/message/details on the other side. The HTTP status code
			// is the wire-level tag (success vs. error); the body carries the
			// structured detail. Retry-After is also set above for HTTP-aware
			// infra (proxies/load balancers) that doesn't read the body.
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(httpStatus)
			body, err := protojson.Marshal(s.Proto())
			if err != nil {
				log.Printf("encoding status proto: %v", err)
				return
			}
			if _, err := rw.Write(body); err != nil {
				log.Printf("writing status body: %v", err)
			}
			return
		}
		if o != nil {
			if err := responder(rw, o); err != nil {
				log.Println(errors.Wrap(err, "encoding response"))
				http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}
	}
}

type Translator[O act.Input] func(*http.Request) (O, error)

// Translate applies a Translator on the Request to populate the Request.URL params.
func Translate[O act.Input](t Translator[O], h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		m, err := t(r)
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
