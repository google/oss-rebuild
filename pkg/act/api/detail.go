// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"log"
	"net/url"

	"github.com/google/oss-rebuild/pkg/act/api/form"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// detailDomain is the ErrorInfo domain of every Detail this system attaches.
const detailDomain = "oss-rebuild.dev"

// Detail is structured data a handler attaches to a status error, encoded
// with the same form tags as a request.
type Detail interface {
	// Reason is the cause of the error the detail accompanies, in the
	// upper-snake form of an ErrorInfo reason. It selects the detail on
	// decode, so it must not change once a service emits it.
	Reason() string
}

// AsDetail renders d as an ErrorInfo detail for AsStatus. A d that the form
// codec cannot encode is logged and dropped.
func AsDetail(d Detail) proto.Message {
	values, err := form.Marshal(d)
	if err != nil {
		log.Printf("Skipping detail which failed to encode: reason=%s,err=%v", d.Reason(), err)
		return nil
	}
	md := make(map[string]string, len(values))
	for k := range values {
		md[k] = values.Get(k)
	}
	return &errdetails.ErrorInfo{Reason: d.Reason(), Domain: detailDomain, Metadata: md}
}

// DetailOf returns the T attached to err, or to a status anywhere in its
// chain, and whether one was found.
func DetailOf[T Detail](err error) (T, bool) {
	var out T
	for _, d := range status.Convert(err).Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.Domain != detailDomain || info.Reason != out.Reason() {
			continue
		}
		values := make(url.Values, len(info.Metadata))
		for k, v := range info.Metadata {
			values.Set(k, v)
		}
		if err := form.Unmarshal(values, &out); err != nil {
			log.Printf("Skipping detail which failed to decode: reason=%s,err=%v", info.Reason, err)
			var zero T
			return zero, false
		}
		return out, true
	}
	return out, false
}
