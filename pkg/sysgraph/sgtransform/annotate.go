// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Network annotation enriches a sysgraph with HTTP metadata from a netlog.
//
// A sysgraph captures kernel-level observations (processes, files, network
// connections via tetragon) while a netlog captures application-level HTTP
// requests from the proxy. These are independent artifacts and not all builds
// produce both. When both are available, AnnotateNetwork performs a post-hoc
// join to associate HTTP metadata with the kernel-observed connections.
//
// The join key is the source port: each tcp_connect kprobe records a network
// address resource ("saddr:sport->daddr:dport"), and each netlog entry
// records the peer's source port. AnnotateNetwork matches these to produce a
// non-mutating SysGraph decorator whose Action method merges HTTP metadata
// on read, leaving the underlying graph untouched.
//
// Because a single action (process) may make many network connections, each
// annotation is keyed by the network resource's digest hash to avoid
// collisions:
//
//	<digest_hash>.http.method = "GET"
//	<digest_hash>.http.scheme = "https"
//	<digest_hash>.http.host   = "registry.npmjs.org"
//	<digest_hash>.http.path   = "/npm"

package sgtransform

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
)

// NetworkAnnotatedSysGraph is a sysgraph view that enriches network actions
// with HTTP metadata from netlog entries.
type NetworkAnnotatedSysGraph struct {
	SysGraph
	// actionMetadata maps action ID → extra metadata key/value pairs to merge.
	actionMetadata map[int64]map[string]string
}

var _ SysGraph = (*NetworkAnnotatedSysGraph)(nil)

// AnnotateNetwork creates a view over sg that enriches actions with HTTP
// metadata from netlog entries, joined via source port matching.
func AnnotateNetwork(ctx context.Context, sg SysGraph, entries []netlog.HTTPRequestLog) (*NetworkAnnotatedSysGraph, error) {
	// Build map: PeerPort → *HTTPRequestLog.
	portMap := make(map[string]*netlog.HTTPRequestLog, len(entries))
	for i := range entries {
		if entries[i].PeerPort != "" {
			portMap[entries[i].PeerPort] = &entries[i]
		}
		// TODO: Detect port reuse and use time-based heuristic
	}
	// Scan all actions to build the metadata overlay.
	actionMetadata := make(map[int64]map[string]string)
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, aid := range aids {
		action, err := sg.Action(ctx, aid)
		if err != nil {
			return nil, err
		}
		for digestStr := range action.GetOutputs() {
			dg, err := pbdigest.NewFromString(digestStr)
			if err != nil {
				continue
			}
			r, err := sg.Resource(ctx, dg)
			if err != nil {
				continue
			}
			if r.GetType() != sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS {
				continue
			}
			addr := r.GetNetworkAddrInfo().GetAddress()
			sport, err := extractSourcePort(addr)
			if err != nil {
				continue
			}
			entry, ok := portMap[sport]
			if !ok {
				continue
			}
			if actionMetadata[aid] == nil {
				actionMetadata[aid] = make(map[string]string)
			}
			prefix := dg.Hash + "."
			actionMetadata[aid][prefix+"http.method"] = entry.Method
			actionMetadata[aid][prefix+"http.scheme"] = entry.Scheme
			actionMetadata[aid][prefix+"http.host"] = entry.Host
			actionMetadata[aid][prefix+"http.path"] = entry.Path
		}
	}
	return &NetworkAnnotatedSysGraph{
		SysGraph:       sg,
		actionMetadata: actionMetadata,
	}, nil
}

// Action returns the action with the given id, enriched with HTTP metadata
// if a netlog match was found.
func (n *NetworkAnnotatedSysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	a, err := n.SysGraph.Action(ctx, id)
	if err != nil {
		return nil, err
	}
	extra, ok := n.actionMetadata[id]
	if !ok {
		return a, nil
	}
	a = proto.Clone(a).(*sgpb.Action)
	md := a.GetMetadata()
	if md == nil {
		md = make(map[string]string)
	}
	maps.Copy(md, extra)
	a.SetMetadata(md)
	return a, nil
}

// extractSourcePort parses the source port from a tcp_connect address string
// of the format "saddr:sport->daddr:dport".
func extractSourcePort(address string) (string, error) {
	// Split on "->" to get "saddr:sport" and "daddr:dport".
	parts := strings.SplitN(address, "->", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid address format: %s", address)
	}
	src := parts[0]
	// Extract port from "saddr:sport".
	idx := strings.LastIndex(src, ":")
	if idx < 0 {
		return "", fmt.Errorf("no port in source address: %s", src)
	}
	return src[idx+1:], nil
}
