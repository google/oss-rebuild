// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"archive/zip"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

var (
	fileRes = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	file2Res = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file2"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/100"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	tcpRes = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
		NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
			Protocol: proto.String("tcp"),
			Address:  proto.String("127.0.0.1:8080"),
		}.Build(),
	}.Build()
)

func TestBuildAndWrite(t *testing.T) {
	builder := SysGraphBuilder{
		GraphPb: sgpb.SysGraph_builder{
			Id: proto.String("abcdefg"),
			Metadata: map[string]string{
				"foo": "bar",
				"baz": "qux",
			},
		}.Build(),
	}
	a0 := builder.Action("0")
	a0.StartTime = time.Unix(1, 1)
	a0.EndTime = time.Unix(10, 10)
	a0.AddInput(fileRes, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1 := builder.Action("1")
	a1.StartTime = time.Unix(2, 2)
	a1.EndTime = time.Unix(3, 3)
	a1.AddInput(tcpRes, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1.AddOutput(file2Res, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1.SetParent("0", sgpb.ActionInteraction_builder{
		Timestamp: &tpb.Timestamp{Seconds: 1, Nanos: 1},
	}.Build())
	sg := builder.Build(context.Background())
	outDir := t.TempDir()
	if err := Write(context.Background(), sg, outDir); err != nil {
		t.Fatalf("Failed to write sysgraph: %v", err)
	}
	expectedsgPath := writeDirFromFs(t, testdata, filepath.Join(testdatagDir, "sysgraph_a"))
	filepath.WalkDir(expectedsgPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("Failed to walk expected sysgraph: %v", err)
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".pb" {
			return nil
		}
		relPath, err := filepath.Rel(expectedsgPath, path)
		if err != nil {
			t.Fatalf("Failed to get relative path for %s: %v", path, err)
		}
		expected, err := os.ReadFile(filepath.Join(expectedsgPath, relPath))
		if err != nil {
			t.Fatalf("Failed to read expected sysgraph file: %v", err)
		}
		actual, err := os.ReadFile(filepath.Join(outDir, relPath))
		if err != nil {
			t.Fatalf("Failed to read actual sysgraph file: %v", err)
		}
		if strings.HasPrefix(relPath, ActionDirName) {
			checkAsProto[*sgpb.Action](t, expected, actual)
		}
		if strings.HasPrefix(relPath, RDBProtoFileName) {
			checkAsProto[*sgpb.ResourceDB](t, expected, actual)
		}
		if strings.HasPrefix(relPath, GraphProtoFileName) {
			checkAsProto[*sgpb.SysGraph](t, expected, actual)
		}
		return nil
	})
}

func TestBuildAndWriteZip(t *testing.T) {
	builder := SysGraphBuilder{
		GraphPb: sgpb.SysGraph_builder{
			Id: proto.String("abcdefg"),
			Metadata: map[string]string{
				"foo": "bar",
				"baz": "qux",
			},
		}.Build(),
	}
	a0 := builder.Action("0")
	a0.StartTime = time.Unix(1, 1)
	a0.EndTime = time.Unix(10, 10)
	a0.AddInput(fileRes, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1 := builder.Action("1")
	a1.StartTime = time.Unix(2, 2)
	a1.EndTime = time.Unix(3, 3)
	a1.AddInput(tcpRes, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1.AddOutput(file2Res, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	a1.SetParent("0", sgpb.ActionInteraction_builder{
		Timestamp: &tpb.Timestamp{Seconds: 1, Nanos: 1},
	}.Build())
	sg := builder.Build(context.Background())
	outZip := filepath.Join(t.TempDir(), "out.zip")
	logsDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create logs dir: %v", err)
	}
	logFile1 := filepath.Join(logsDir, "log1.log")
	if err := os.WriteFile(logFile1, []byte("this is tetragon stdout logs"), 0644); err != nil {
		t.Fatalf("Failed to create log file 1: %v", err)
	}
	policiesDir := filepath.Join(logsDir, "policies")
	if err := os.MkdirAll(policiesDir, 0755); err != nil {
		t.Fatalf("Failed to create policies dir: %v", err)
	}
	logFile2 := filepath.Join(policiesDir, "policy1.json")
	if err := os.WriteFile(logFile2, []byte("this is tetragon tracing policy"), 0644); err != nil {
		t.Fatalf("Failed to create log file 2: %v", err)
	}
	if err := Write(context.Background(), sg, outZip, CopyPath(logsDir, "tetragon_logs")); err != nil {
		t.Fatalf("Failed to write sysgraph: %v", err)
	}
	expectedsgPath := writeDirFromFs(t, testdata, filepath.Join(testdatagDir, "sysgraph_a"))
	zipReader, err := zip.OpenReader(outZip)
	if err != nil {
		t.Fatalf("Failed to open zip file: %v", err)
	}
	defer zipReader.Close()
	foundLogs := map[string]bool{
		"tetragon_logs/log1.log":              false,
		"tetragon_logs/policies/policy1.json": false,
	}
	for _, zipFile := range zipReader.File {
		if zipFile.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(zipFile.Name, "tetragon_logs/") {
			actualFile, err := zipFile.Open()
			if err != nil {
				t.Fatalf("Failed to open zip file %s: %v", zipFile.Name, err)
			}
			actual, err := io.ReadAll(actualFile)
			if err != nil {
				t.Fatalf("Failed to read actual sysgraph file %s: %v", zipFile.Name, err)
			}
			if err := actualFile.Close(); err != nil {
				t.Fatalf("Failed to close zip file %s: %v", zipFile.Name, err)
			}
			if zipFile.Name == "tetragon_logs/log1.log" {
				if string(actual) != "this is tetragon stdout logs" {
					t.Errorf("Copied file %s has wrong content: got %q, want %q", zipFile.Name, string(actual), "this is tetragon stdout logs")
				}
				foundLogs[zipFile.Name] = true
			} else if zipFile.Name == "tetragon_logs/policies/policy1.json" {
				if string(actual) != "this is tetragon tracing policy" {
					t.Errorf("Copied file %s has wrong content: got %q, want %q", zipFile.Name, string(actual), "this is tetragon tracing policy")
				}
				foundLogs[zipFile.Name] = true
			}
			continue
		}
		if filepath.Ext(zipFile.Name) != ".pb" {
			continue
		}
		expected, err := os.ReadFile(filepath.Join(expectedsgPath, zipFile.Name))
		if err != nil {
			t.Errorf("Failed to read expected sysgraph file: %v", err)
		}
		actualFile, err := zipFile.Open()
		if err != nil {
			t.Fatalf("Failed to open zip file: %v", err)
		}
		actual, err := io.ReadAll(actualFile)
		if err != nil {
			t.Fatalf("Failed to read actual sysgraph file: %v", err)
		}
		if err := actualFile.Close(); err != nil {
			t.Fatalf("Failed to close zip file: %v", err)
		}
		if strings.HasPrefix(zipFile.Name, ActionDirName) {
			checkAsProto[*sgpb.Action](t, expected, actual)
		}
		if strings.HasPrefix(zipFile.Name, RDBProtoFileName) {
			checkAsProto[*sgpb.ResourceDB](t, expected, actual)
		}
		if strings.HasPrefix(zipFile.Name, GraphProtoFileName) {
			checkAsProto[*sgpb.SysGraph](t, expected, actual)
		}
	}
	for name, found := range foundLogs {
		if !found {
			t.Errorf("Expected log file %s not found in zip", name)
		}
	}
}

func mustDigest(t *testing.T, m proto.Message) pbdigest.Digest {
	t.Helper()
	dg, err := pbdigest.NewFromMessage(m)
	if err != nil {
		t.Fatalf("Failed to create digest %q: %v", m, err)
	}
	return dg
}

func checkAsProto[T proto.Message](t *testing.T, expected, got []byte) {
	t.Helper()
	var expectedPb T
	// Peek the type inside T (as T= *SomeProtoMsgType)
	msgType := reflect.TypeOf(expectedPb).Elem()
	// Make a new one, and throw it back into T
	expectedPb = reflect.New(msgType).Interface().(T)
	if err := proto.Unmarshal(expected, expectedPb); err != nil {
		t.Errorf("Failed to unmarshal file %s: %v", expected, err)
		return
	}
	gotPb := reflect.New(msgType).Interface().(T)
	if err := proto.Unmarshal(got, gotPb); err != nil {
		t.Errorf("Failed to unmarshal file %s: %v", got, err)
		return
	}
	t.Logf("Comparing %v to %v", expectedPb, gotPb)
	if diff := cmp.Diff(expectedPb, gotPb, protocmp.Transform()); diff != "" {
		t.Errorf("File %s differs from expected, diff %s", expected, diff)
		return
	}
}
