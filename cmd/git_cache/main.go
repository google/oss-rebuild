// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/google/oss-rebuild/internal/gitcache"
)

var (
	cache = flag.String("cache", "", "cache location: gs://bucket-name for GCS, or /path/to/dir for local filesystem")
	port  = flag.Int("port", 8080, "port on which to serve")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), `
Note: The git cache supports checking out submodules but it won't cache them, nor is it used to fetch them.
`)
	}

	flag.Parse()
	if *cache == "" {
		flag.Usage()
		log.Fatalln("Error: -cache flag is required")
	}
	ctx := context.Background()
	s, err := gitcache.NewServer(ctx, *cache)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/get", s.HandleGet)
	log.Printf("Listening on port %d", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		log.Fatalln(err)
	}
}
