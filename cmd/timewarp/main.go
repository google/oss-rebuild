// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// The timewarp binary serves the registry timewarp HTTP handler on a local port.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/google/oss-rebuild/internal/timewarp"
)

var (
	port = flag.Int("port", 8081, "port on which to serve")
)

func main() {
	flag.Parse()
	log.Printf("Server listening on port %d", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), timewarp.Handler{Client: http.DefaultClient}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
