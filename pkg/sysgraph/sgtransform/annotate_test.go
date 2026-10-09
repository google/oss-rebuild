// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
)

func TestAnnotateNetwork(t *testing.T) {
	// Create a network address resource.
	netResource := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
		NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
			Protocol: proto.String("tcp"),
			Address:  proto.String("10.0.0.1:12345->93.184.216.34:443"),
		}.Build(),
	}.Build()
	netDg, err := pbdigest.NewFromMessage(netResource)
	if err != nil {
		t.Fatal(err)
	}
	// Create a file resource (should be ignored).
	fileResource := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path: proto.String("/tmp/test"),
		}.Build(),
	}.Build()
	fileDg, err := pbdigest.NewFromMessage(fileResource)
	if err != nil {
		t.Fatal(err)
	}
	sg := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{1},
		}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Outputs: map[string]*sgpb.ResourceInteractions{
					netDg.String():  sgpb.ResourceInteractions_builder{}.Build(),
					fileDg.String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id: proto.Int64(2),
				Outputs: map[string]*sgpb.ResourceInteractions{
					fileDg.String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
		},
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			netDg:  netResource,
			fileDg: fileResource,
		},
	}
	entries := []netlog.HTTPRequestLog{
		{
			Method:   "GET",
			Scheme:   "https",
			Host:     "example.com",
			Path:     "/api/v1/data",
			PeerPort: "12345",
			Time:     time.Now(),
		},
		{
			Method:   "POST",
			Scheme:   "https",
			Host:     "other.com",
			Path:     "/upload",
			PeerPort: "99999",
			Time:     time.Now(),
		},
	}
	ctx := t.Context()
	annotated, err := AnnotateNetwork(ctx, sg, entries)
	if err != nil {
		t.Fatal(err)
	}
	// Verify action 1 got annotated via the view.
	a1, err := annotated.Action(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	prefix := netDg.Hash + "."
	want := map[string]string{
		prefix + "http.method": "GET",
		prefix + "http.scheme": "https",
		prefix + "http.host":   "example.com",
		prefix + "http.path":   "/api/v1/data",
	}
	if diff := cmp.Diff(want, a1.GetMetadata()); diff != "" {
		t.Errorf("action 1 metadata mismatch (-want +got):\n%s", diff)
	}
	// Verify action 2 was not annotated.
	a2, err := annotated.Action(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if md2 := a2.GetMetadata(); md2 != nil && len(md2) > 0 {
		t.Errorf("expected no metadata on action 2, got %v", md2)
	}
	// Verify the original sysgraph is unmodified.
	origA1, err := sg.Action(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if md := origA1.GetMetadata(); md != nil && len(md) > 0 {
		t.Errorf("expected original action 1 to have no metadata, got %v", md)
	}
}

func TestAnnotateNetwork_NoMatch(t *testing.T) {
	netResource := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
		NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
			Protocol: proto.String("tcp"),
			Address:  proto.String("10.0.0.1:55555->93.184.216.34:443"),
		}.Build(),
	}.Build()
	netDg, err := pbdigest.NewFromMessage(netResource)
	if err != nil {
		t.Fatal(err)
	}
	sg := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Outputs: map[string]*sgpb.ResourceInteractions{
					netDg.String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
		},
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			netDg: netResource,
		},
	}
	entries := []netlog.HTTPRequestLog{
		{
			Method:   "GET",
			Scheme:   "https",
			Host:     "example.com",
			Path:     "/",
			PeerPort: "11111",
			Time:     time.Now(),
		},
	}
	ctx := t.Context()
	annotated, err := AnnotateNetwork(ctx, sg, entries)
	if err != nil {
		t.Fatal(err)
	}
	// No match -> metadata should remain nil.
	a, err := annotated.Action(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if md := a.GetMetadata(); md != nil && len(md) > 0 {
		t.Errorf("expected no metadata, got %v", md)
	}
}

func TestExtractSourcePort(t *testing.T) {
	tests := []struct {
		address string
		want    string
		wantErr bool
	}{
		{"10.0.0.1:12345->93.184.216.34:443", "12345", false},
		{"::1:8080->::1:443", "8080", false},
		{"invalid", "", true},
		{"no-arrow:123", "", true},
	}
	for _, tt := range tests {
		got, err := extractSourcePort(tt.address)
		if (err != nil) != tt.wantErr {
			t.Errorf("extractSourcePort(%q) error = %v, wantErr %v", tt.address, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("extractSourcePort(%q) = %q, want %q", tt.address, got, tt.want)
		}
	}
}
