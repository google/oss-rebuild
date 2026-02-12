// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"maps"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/dynamicpb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

//go:embed testdata/multi_graph testdata/multi_graph_actions testdata/sysgraph_a
var testdata embed.FS

var (
	wantAction0 = sgpb.Action_builder{
		Id:         proto.Int64(1),
		SysGraphId: proto.String("abcdefg"),
		StartTime:  &tpb.Timestamp{Seconds: 1, Nanos: 1},
		EndTime:    &tpb.Timestamp{Seconds: 10, Nanos: 10},
		Inputs: map[string]*sgpb.ResourceInteractions{
			"49ef35511c267514f67ebbb6eac0d2c006cc92677817cc747b67218224395f64/89": sgpb.ResourceInteractions_builder{
				Interactions: []*sgpb.ResourceInteraction{sgpb.ResourceInteraction_builder{
					IoInfo: sgpb.IOInfo_builder{BytesUsed: proto.Uint64(100)}.Build(),
				}.Build()},
			}.Build(),
		},
		Children: map[int64]*sgpb.ActionInteraction{
			2: sgpb.ActionInteraction_builder{
				Timestamp: &tpb.Timestamp{Seconds: 1, Nanos: 1},
			}.Build(),
		},
	}.Build()

	wantAction1 = sgpb.Action_builder{
		Id:         proto.Int64(2),
		SysGraphId: proto.String("abcdefg"),
		StartTime:  &tpb.Timestamp{Seconds: 2, Nanos: 2},
		EndTime:    &tpb.Timestamp{Seconds: 3, Nanos: 3},
		Inputs: map[string]*sgpb.ResourceInteractions{
			"5df334548b8ba87841189714473268e64cc306042ceb70b2c9e28a55da2fbe04/25": sgpb.ResourceInteractions_builder{
				Interactions: []*sgpb.ResourceInteraction{sgpb.ResourceInteraction_builder{
					IoInfo: sgpb.IOInfo_builder{BytesUsed: proto.Uint64(100)}.Build(),
				}.Build()},
			}.Build(),
		},
		Outputs: map[string]*sgpb.ResourceInteractions{
			"f9c2c3380d4de562fb87db83108822814e88197aad8d482709e615e963a031d1/91": sgpb.ResourceInteractions_builder{
				Interactions: []*sgpb.ResourceInteraction{sgpb.ResourceInteraction_builder{
					IoInfo: sgpb.IOInfo_builder{BytesUsed: proto.Uint64(100)}.Build(),
				}.Build()},
			}.Build(),
		},
		ParentActionId: proto.Int64(1),
		Parent: sgpb.ActionInteraction_builder{
			Timestamp: &tpb.Timestamp{Seconds: 1, Nanos: 1},
		}.Build(),
	}.Build()

	wantRDB = map[pbdigest.Digest]*sgpb.Resource{
		{
			Hash: "49ef35511c267514f67ebbb6eac0d2c006cc92677817cc747b67218224395f64",
			Size: 89,
		}: sgpb.Resource_builder{
			Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
			FileInfo: sgpb.FileInfo_builder{
				Path:   proto.String("path/to/file"),
				Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
				Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
			}.Build(),
		}.Build(),
		{
			Hash: "5df334548b8ba87841189714473268e64cc306042ceb70b2c9e28a55da2fbe04",
			Size: 25,
		}: sgpb.Resource_builder{
			Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
			NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
				Protocol: proto.String("tcp"),
				Address:  proto.String("127.0.0.1:8080"),
			}.Build(),
		}.Build(),
		{
			Hash: "f9c2c3380d4de562fb87db83108822814e88197aad8d482709e615e963a031d1",
			Size: 91,
		}: sgpb.Resource_builder{
			Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
			FileInfo: sgpb.FileInfo_builder{
				Path:   proto.String("path/to/file2"),
				Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/100"),
				Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
			}.Build(),
		}.Build(),
	}
)

// TestLoadSysGraph tests loading a sysgraph from
// from ./testdata/sysgraph_a
func TestLoadSysGraph(t *testing.T) {
	context.Background()
	testCases := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "dir",
			path: func(t *testing.T) string {
				return writeDirFromFs(t, testdata, "testdata/sysgraph_a")
			},
		},
		{
			name: "zip",
			path: func(t *testing.T) string {
				return writeZipFromFs(t, testdata, "testdata/sysgraph_a")
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sg, err := LoadSysGraph(ctx, tc.path(t))
			if err != nil {
				t.Fatalf("Failed to load sysgraph: %v", err)
			}
			want := []int64{1, 2}
			got, err := sg.ActionIDs(ctx)
			if err != nil {
				t.Fatalf("Failed to get action ids: %v", err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ActionIds() = %v, want %v, diff %s", got, want, diff)
			}

			gotAction0, err := sg.Action(ctx, 1)
			if err != nil {
				t.Errorf("Action(1) = %v, want nil", err)
			}
			if diff := cmp.Diff(wantAction0, gotAction0, protocmp.Transform(), protocmp.IgnoreDefaultScalars()); diff != "" {
				t.Errorf("Action(1) = %v, want %v, diff %s", gotAction0, wantAction0, diff)
			}

			gotAction1, err := sg.Action(ctx, 2)
			if err != nil {
				t.Errorf("Action(1) = %v, want nil", err)
			}
			if diff := cmp.Diff(wantAction1, gotAction1, protocmp.Transform()); diff != "" {
				t.Errorf("Action(1) = %v, want %v, diff %s", gotAction1, wantAction1, diff)
			}
			for dg, wantR := range wantRDB {
				gotR, err := sg.Resource(ctx, dg)
				if err != nil {
					t.Errorf("Resource(%s) = %v, want nil", dg, err)
					continue
				}
				if diff := cmp.Diff(wantR, gotR, protocmp.Transform()); diff != "" {
					t.Errorf("Resource(%s) = %v, want %v, diff %s", dg, gotR, wantR, diff)
				}
			}
			gotDgs, err := sg.ResourceDigests(ctx)
			if err != nil {
				t.Fatalf("Failed to get resource digests: %v", err)
			}
			slices.SortFunc(gotDgs, func(a, b pbdigest.Digest) int {
				return strings.Compare(a.String(), b.String())
			})
			wantDgs := slices.Collect(maps.Keys(wantRDB))
			slices.SortFunc(wantDgs, func(a, b pbdigest.Digest) int {
				return strings.Compare(a.String(), b.String())
			})
			if diff := cmp.Diff(wantDgs, gotDgs); diff != "" {
				t.Errorf("ResourceDigests() = %v, want %v, diff %s", gotDgs, wantDgs, diff)
			}
			gotResources, err := sg.Resources(ctx)
			if err != nil {
				t.Fatalf("Failed to get resources: %v", err)
			}
			if diff := cmp.Diff(wantRDB, gotResources, protocmp.Transform()); diff != "" {
				t.Errorf("Resources() = %v, want %v, diff %s", gotResources, wantRDB, diff)
			}
			wantProto := sgpb.SysGraph_builder{
				Id: proto.String("abcdefg"),
				Metadata: map[string]string{
					"foo": "bar",
					"baz": "qux",
				},
				EntryPointActionIds: []int64{1},
			}.Build()
			gotProto := sg.Proto(ctx)
			if diff := cmp.Diff(wantProto, gotProto, protocmp.Transform()); diff != "" {
				t.Errorf("Proto() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadMultiGraph(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string
	}{
		{
			name: "dir",
			path: func(t *testing.T) string {
				return writeDirFromFs(t, testdata, "testdata/multi_graph")
			},
		},
		{
			name: "zip",
			path: func(t *testing.T) string {
				return writeZipFromFs(t, testdata, "testdata/multi_graph")
			},
		},
		{
			name: "dir with actions in base graph",
			path: func(t *testing.T) string {
				return writeDirFromFs(t, testdata, "testdata/multi_graph_actions")
			},
			wantErr: "base graph has 2 actions, multi-step graphs must have an empty base graph",
		},
		{
			name: "zip with actions in base graph",
			path: func(t *testing.T) string {
				return writeZipFromFs(t, testdata, "testdata/multi_graph_actions")
			},
			wantErr: "base graph has 2 actions, multi-step graphs must have an empty base graph",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sg, err := LoadSysGraph(ctx, tc.path(t))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("LoadSysGraph() succeeded, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadSysGraph() returned error %q, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Failed to load sysgraph: %v", err)
			}

			wantActionIDs := []int64{1, 2, 3, 4, 5}
			gotActionIDs, err := sg.ActionIDs(ctx)
			if err != nil {
				t.Fatalf("Failed to get action ids: %v", err)
			}
			if diff := cmp.Diff(wantActionIDs, gotActionIDs); diff != "" {
				t.Errorf("ActionIDs() = %v, want %v, diff %s", gotActionIDs, wantActionIDs, diff)
			}

			wantEntryPointIDs := []int64{1, 3}
			gotEntryPointIDs := sg.Proto(ctx).GetEntryPointActionIds()
			if diff := cmp.Diff(wantEntryPointIDs, gotEntryPointIDs); diff != "" {
				t.Errorf("EntryPointActionIds() = %v, want %v, diff %s", gotEntryPointIDs, wantEntryPointIDs, diff)
			}

			for _, aid := range wantActionIDs {
				a, err := sg.Action(ctx, aid)
				if err != nil {
					t.Fatalf("failed to get action %d: %v", aid, err)
				}
				if a.GetId() != aid {
					t.Errorf("a.GetId() = %d, want %d", a.GetId(), aid)
				}
			}

			// Action 2 from sg1 should have a global ID of 2.
			// Its parent ID should be offset to 1.
			a2, err := sg.Action(ctx, 2)
			if err != nil {
				t.Fatalf("failed to get action 2: %v", err)
			}
			if a2.GetId() != 2 {
				t.Errorf("a2.GetId() = %d, want 2", a2.GetId())
			}
			if a2.GetParentActionId() != 1 {
				t.Errorf("a2.GetParentActionId() = %d, want 1", a2.GetParentActionId())
			}

			// Action 1 from sg2 should have a global ID of 3.
			a3, err := sg.Action(ctx, 3)
			if err != nil {
				t.Fatalf("failed to get action 3: %v", err)
			}
			if a3.GetId() != 3 {
				t.Errorf("a3.GetId() = %d, want 3", a3.GetId())
			}
			// Check that children were offset correctly.
			wantChildActionIDs := []int64{4, 5}
			gotChildActionIDs := slices.Collect(maps.Keys(a3.GetChildren()))
			slices.Sort(gotChildActionIDs)
			if diff := cmp.Diff(wantChildActionIDs, gotChildActionIDs); diff != "" {
				t.Errorf("a3.GetChildren() keys returned unexpected diff (-want +got):\n%s", diff)
			}

			a4, err := sg.Action(ctx, 4)
			if err != nil {
				t.Fatalf("failed to get action 4: %v", err)
			}
			if a4.GetId() != 4 {
				t.Errorf("a4.GetId() = %d, want 4", a4.GetId())
			}
			if a4.GetParentActionId() != 3 {
				t.Errorf("a4.GetParentActionId() = %d, want 3", a4.GetParentActionId())
			}

			// Check that the resource digests and resources are correct.
			gotDgs, err := sg.ResourceDigests(ctx)
			if err != nil {
				t.Fatalf("Failed to get resource digests: %v", err)
			}
			slices.SortFunc(gotDgs, func(a, b pbdigest.Digest) int {
				return strings.Compare(a.String(), b.String())
			})
			wantDgs := slices.Collect(maps.Keys(wantRDB))
			slices.SortFunc(wantDgs, func(a, b pbdigest.Digest) int {
				return strings.Compare(a.String(), b.String())
			})
			if diff := cmp.Diff(wantDgs, gotDgs); diff != "" {
				t.Errorf("ResourceDigests() = %v, want %v, diff %s", gotDgs, wantDgs, diff)
			}
			gotResources, err := sg.Resources(ctx)
			if err != nil {
				t.Fatalf("Failed to get resources: %v", err)
			}
			if diff := cmp.Diff(wantRDB, gotResources, protocmp.Transform()); diff != "" {
				t.Errorf("Resources() = %v, want %v, diff %s", gotResources, wantRDB, diff)
			}
		})
	}
}

func writeDirFromFs(t *testing.T, f fs.FS, path string) string {
	t.Helper()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, filepath.Base(path))
	if err := os.MkdirAll(outPath, 0755); err != nil {
		t.Fatalf("Failed to create directory %v", err)
	}
	if err := fs.WalkDir(f, path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		outFilePath := filepath.Join(outPath, strings.TrimPrefix(p, path))
		if err := os.MkdirAll(filepath.Dir(outFilePath), 0755); err != nil {
			return fmt.Errorf("Failed to create outFilePath %v", err)
		}
		t.Logf("Copying %s to %s", p, outFilePath)
		inFile, err := f.Open(p)
		if err != nil {
			return fmt.Errorf("Failed to open file %v", err)
		}
		defer inFile.Close()
		inReader := io.Reader(inFile)
		if filepath.Ext(p) == ".txtpb" {
			outFilePath = strings.TrimSuffix(outFilePath, ".txtpb") + ".pb"
			txtBlob, err := io.ReadAll(inReader)
			if err != nil {
				return fmt.Errorf("Failed to read file %v", err)
			}
			binaryBlob, err := txtPbToProto(txtBlob)
			if err != nil {
				return fmt.Errorf("Failed to convert txtpb to proto %v", err)
			}
			inReader = bytes.NewReader(binaryBlob)
		}
		outFile, err := os.Create(outFilePath)
		if err != nil {
			return fmt.Errorf("Failed to create file %v", err)
		}
		defer outFile.Close()
		if _, err := io.Copy(outFile, inReader); err != nil {
			return fmt.Errorf("Failed to copy file %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Failed to walk directory %v", err)
	}
	t.Logf("Wrote directory %s", outPath)
	return outPath
}

func writeZipFromFs(t *testing.T, f fs.FS, path string) string {
	t.Helper()
	dir := writeDirFromFs(t, f, path)
	outPath := filepath.Join(t.TempDir(), filepath.Base(dir)+".zip")
	zipFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("Failed to create zip file %v", err)
	}
	defer zipFile.Close()
	zipWriter := zip.NewWriter(zipFile)
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		p = strings.TrimPrefix(p, fmt.Sprintf("%s%c", dir, os.PathSeparator))
		if strings.HasPrefix(p, dir) {
			return nil
		}
		if d.IsDir() {
			// add a trailing slash for creating directory in zip
			p = fmt.Sprintf("%s%c", p, os.PathSeparator)
			_, err = zipWriter.Create(p)
			return nil
		}
		blob, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return err
		}
		w, err := zipWriter.Create(p)
		if err != nil {
			return err
		}
		if _, err := w.Write(blob); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to walk testdata: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}
	return outPath
}


// txtPbToProto converts a text proto blob to a binary proto blob using the proto-message header.
func txtPbToProto(txtBlob []byte) ([]byte, error) {
	for line := range strings.SplitSeq(string(txtBlob), "\n") {
		if strings.Contains(line, "proto-message:") {
			messageName := "sysgraph." + strings.TrimSpace(strings.Split(line, "proto-message:")[1])
			descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
			if err != nil {
				return nil, err
			}
			messageDescriptor, ok := descriptor.(protoreflect.MessageDescriptor)
			if !ok {
				return nil, fmt.Errorf("not a message descriptor: %s", messageName)
			}
			message := dynamicpb.NewMessage(messageDescriptor)
			if err := prototext.Unmarshal(txtBlob, message); err != nil {
				return nil, err
			}
			return proto.Marshal(message)
		}
	}
	return nil, fmt.Errorf("could not find message name in txtpb")
}
