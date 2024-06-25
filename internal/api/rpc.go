package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/schema/form"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Dependencies interface{}

type InitT[D Dependencies] func(context.Context) (D, error)
type HandlerT[I schema.Message, O any, D Dependencies] func(context.Context, I, D) (*O, error)
type StubT[I schema.Message, O any] func(context.Context, I) (*O, error)

type NoDeps struct{}

func NoDepsInit(context.Context) (*NoDeps, error) { return &NoDeps{}, nil }

type NoReturn struct{}

var ErrNotOK = errors.New("non-OK response")

func Stub[I schema.Message, O any](client httpinternal.BasicClient, u url.URL) StubT[I, O] {
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
			return nil, errors.Wrap(ErrNotOK, resp.Status)
		}
		var o O
		if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
			return nil, errors.Wrap(err, "decoding response")
		}
		return &o, nil
	}
}

func StubFromHandler[I schema.Message, O any, D Dependencies](client httpinternal.BasicClient, u url.URL, handler HandlerT[I, O, D]) StubT[I, O] {
	return Stub[I, O](client, u)
}

func AsStatus(code codes.Code, err error) error {
	return status.New(code, err.Error()).Err()
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

func Handler[I schema.Message, O any, D Dependencies](initDeps InitT[D], handler HandlerT[I, O, D]) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		r.ParseForm()
		var req I
		if err := form.Unmarshal(r.Form, &req); err != nil {
			log.Println(errors.Wrap(err, "parsing request"))
			http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
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
		status, ok := grpcToHTTP[s.Code()]
		if !ok {
			log.Printf("unknown error code: %s\n", s.Code())
			status = http.StatusInternalServerError
		}
		if status != http.StatusOK {
			log.Println(s.Err())
			http.Error(rw, http.StatusText(status), status)
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
