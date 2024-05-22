// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), timewarp.Handler{}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
