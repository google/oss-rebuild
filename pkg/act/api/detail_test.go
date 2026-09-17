// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type whereDetail struct {
	Place string    `form:"place"`
	When  time.Time `form:"when"`
}

func (whereDetail) Reason() string { return "WHERE" }

type otherDetail struct{ X string }

func (otherDetail) Reason() string { return "OTHER" }

func TestDetailRoundTrip(t *testing.T) {
	want := whereDetail{Place: "here", When: time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)}
	h := func(ctx context.Context, req FooRequest, _ *NoDeps) (*FooResponse, error) {
		return nil, AsStatus(codes.Internal, errors.New("nope"), AsDetail(want))
	}
	server := httptest.NewServer(Handler(NoDepsInit, h))
	defer server.Close()
	for name, stub := range map[string]StubFn[FooRequest, FooResponse]{
		"remote": Stub[FooRequest, FooResponse](server.Client(), urlx.MustParse(server.URL)),
		"local":  Local(h, &NoDeps{}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stub(context.Background(), FooRequest{Foo: "foo"})
			err = errors.Wrap(err, "calling")
			if st := status.Convert(err); st.Code() != codes.Internal || st.Message() != "calling: rpc error: code = Internal desc = nope" {
				t.Errorf("status = %v", st.Err())
			}
			got, ok := DetailOf[whereDetail](err)
			if !ok || got.Place != want.Place || !got.When.Equal(want.When) {
				t.Errorf("DetailOf = %+v, %v; want %+v, true", got, ok, want)
			}
			if _, ok := DetailOf[otherDetail](err); ok {
				t.Error("DetailOf found a detail of another reason")
			}
		})
	}
	if _, ok := DetailOf[whereDetail](errors.New("plain")); ok {
		t.Error("DetailOf found a detail on a plain error")
	}
}
