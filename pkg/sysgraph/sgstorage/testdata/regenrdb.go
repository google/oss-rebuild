// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// regenrdb regenerates the rdb.txtpb file in the testdata directory.
package main

import (
	"fmt"
	"os"

	"flag"

	"log"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/protocolbuffers/txtpbfmt/parser"
	"google.golang.org/protobuf/encoding/prototext"
)

var (
	outputPath = flag.String("output_path", "", "The workspace directory")
	rdbPath    = flag.String("rdb_path", "", "The path to the rdb.txtpb file")
)

const textProtoHeader = `# proto-file: github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph/sysgraph.proto
# proto-message: ResourceDB
`

func main() {
	flag.Parse()
	rdb, err := os.ReadFile(*rdbPath)
	if err != nil {
		log.Fatalf("Failed to read rdb.txtpb: %v", err)
	}
	rdbpb := &sgpb.ResourceDB{}
	if err := prototext.Unmarshal(rdb, rdbpb); err != nil {
		log.Fatalf("Failed to unmarshal rdb.txtpb: %v", err)
	}
	correctResources := make(map[string]*sgpb.Resource, len(rdbpb.GetResources()))
	for _, r := range rdbpb.GetResources() {
		dg, err := pbdigest.NewFromMessage(r)
		if err != nil {
			log.Fatalf("Failed to get digest from resource: %v", err)
		}
		correctResources[dg.String()] = r
	}
	rdbpb = sgpb.ResourceDB_builder{
		Resources: correctResources,
	}.Build()
	blob, err := prototext.MarshalOptions{Multiline: true}.Marshal(rdbpb)
	if err != nil {
		log.Fatalf("Failed to marshal rdb.txtpb: %v", err)
	}
	formattedBlob, err := parser.Format(blob)
	if err != nil {
		log.Fatalf("Failed to format rdb.txtpb: %v", err)
	}
	fileContent := fmt.Sprintf("%s\n%s", textProtoHeader, string(formattedBlob))
	if err := os.WriteFile(*outputPath, []byte(fileContent), 0644); err != nil {
		log.Fatalf("Failed to write rdb.txtpb: %v", err)
	}
}
