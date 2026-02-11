// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	_ "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

//go:generate go run gen.go

var graphToZip = []string{
	"sysgraph_a",
	"multi_graph",
	"multi_graph_actions",
}

func findMessageName(txtBlob []byte) string {
	for line := range strings.SplitSeq(string(txtBlob), "\n") {
		if strings.Contains(line, "proto-message:") {
			return "sysgraph." + strings.TrimSpace(strings.Split(line, "proto-message:")[1])
		}
	}
	return ""
}

func main() {
	if err := mainx(); err != nil {
		fmt.Fprintln(os.Stderr, "Error: ", err)
		os.Exit(1)
	}
}

func cleanGenFiles() error {
	fis, err := os.ReadDir("./")
	if err != nil {
		return err
	}
	for _, fi := range fis {
		if fi.Name() == "gen.go" {
			continue
		}
		if err := os.RemoveAll(fi.Name()); err != nil {
			return err
		}
	}
	return nil
}

func mainx() error {
	fmt.Fprintln(os.Stderr, "Cleaning up...")
	if err := cleanGenFiles(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Generating test data...")

	testDataDir, err := filepath.Abs("../testdata")
	if err != nil {
		return err
	}

	if err := filepath.Walk(testDataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".txtpb") {
			return nil
		}
		txtBlob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		messageName := findMessageName(txtBlob)
		if messageName == "" {
			return fmt.Errorf("could not find message name in %s", path)
		}
		descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(messageName))
		if err != nil {
			return err
		}
		messageDescriptor, ok := descriptor.(protoreflect.MessageDescriptor)
		if !ok {
			return fmt.Errorf("not a message descriptor: %s", messageName)
		}
		message := dynamicpb.NewMessage(messageDescriptor)
		if err := prototext.Unmarshal(txtBlob, message); err != nil {
			return err
		}
		binaryBlob, err := proto.Marshal(message)
		if err != nil {
			return err
		}
		outPath, err := filepath.Rel(testDataDir, path)
		if err != nil {
			return err
		}
		outFilePath := strings.TrimSuffix(outPath, ".txtpb") + ".pb"
		if err := os.MkdirAll(filepath.Dir(outFilePath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(outFilePath, binaryBlob, 0644); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	for _, graph := range graphToZip {
		zipPath := filepath.Join(graph, graph+".zip")
		fmt.Fprintln(os.Stderr, "Generating "+zipPath)
		if err := os.MkdirAll(filepath.Dir(zipPath), 0755); err != nil {
			return err
		}
		zipFile, err := os.Create(zipPath)
		if err != nil {
			return err
		}
		zipWriter := zip.NewWriter(zipFile)
		if err := filepath.WalkDir(graph, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Adding "+path)
			path = strings.TrimPrefix(path, fmt.Sprintf("%s%c", graph, os.PathSeparator))
			if strings.HasPrefix(path, graph) {
				return nil
			}
			if d.IsDir() {
				// add a trailing slash for creating dir
				path = fmt.Sprintf("%s%c", path, os.PathSeparator)
				_, err = zipWriter.Create(path)
				return nil
			}
			blob, err := os.ReadFile(filepath.Join(graph, path))
			if err != nil {
				return err
			}
			w, err := zipWriter.Create(path)
			if err != nil {
				return err
			}
			if _, err := w.Write(blob); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if err := zipWriter.Close(); err != nil {
			return err
		}
	}

	return nil
}
