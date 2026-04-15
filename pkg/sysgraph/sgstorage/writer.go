// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"log"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

const (
	bufferSize = 100 * 1024 * 1024 // 100 MB
)

// SysGraphProvider provides methods for loading a sysgraph.
type SysGraphProvider interface {
	sgquery.ActionProvider
	Proto(context.Context) *sgpb.SysGraph
	Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error)
	RawEvents(context.Context) ([]*anypb.Any, error)
	RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error)
}

// Write loads the sysgraph from the given proto definition.
func Write(ctx context.Context, sg SysGraphProvider, graphPath string, opts ...Option) error {
	log.Printf("Writing sysgraph to %s", graphPath)
	writer, err := NewGraphWriter(ctx, graphPath, opts...)
	if err != nil {
		return err
	}
	closeWriter := sync.OnceValue(writer.Close)
	defer closeWriter()
	rdgs, err := sg.Resources(ctx)
	if err != nil {
		return err
	}
	strRdgs := make(map[string]*sgpb.Resource, len(rdgs))
	for dg, r := range rdgs {
		strRdgs[dg.String()] = r
	}
	rdb := sgpb.ResourceDB_builder{
		Resources: strRdgs,
	}.Build()
	if err := writer.WriteRDB(ctx, rdb); err != nil {
		return err
	}
	if err := writer.WriteGraphProto(ctx, sg.Proto(ctx)); err != nil {
		return err
	}
	aids, err := sg.ActionIDs(ctx)
	log.Printf("Writing %d actions", len(aids))
	if err != nil {
		return err
	}
	if slices.Contains(aids, 0) {
		return fmt.Errorf("action id 0 is not valid, all action ids must be non-zero")
	}
	if err := sgquery.RangeActions(ctx, sg, func(ctx context.Context, a *sgpb.Action) error {
		if err := writer.WriteAction(ctx, a); err != nil {
			return err
		}
		rawEvents, err := sg.RawEventsForAction(ctx, a.GetId())
		if err != nil {
			return err
		}
		if len(rawEvents) > 0 {
			ch := make(chan *anypb.Any, len(rawEvents))
			go func() {
				defer close(ch)
				for _, event := range rawEvents {
					ch <- event
				}
			}()
			if err := writer.WriteRawEvents(ctx, a.GetId(), ch); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	log.Println("Copying tetragon logs into sysgraph.")
	for _, op := range writer.copyOps {
		err := filepath.WalkDir(op.src, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			relPath, err := filepath.Rel(op.src, path)
			if err != nil {
				return err
			}
			destPath := filepath.Join(op.dst, relPath)
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return writer.fs.WriteFile(ctx, destPath, content)
		})
		if err != nil {
			return err
		}
	}

	log.Printf("Done writing sysgraph to %s", graphPath)
	return closeWriter()
}
