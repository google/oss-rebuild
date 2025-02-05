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

// Package main builds and runs a rebuild server.
package main

import (
	"context"
	"log"

	"github.com/google/oss-rebuild/build/container"
	"github.com/google/oss-rebuild/tools/docker"
)

func main() {
	ctx := context.Background()

	err := container.Build(ctx, "rebuilder")
	if err != nil {
		log.Fatal(err)
	}

	idchan := make(chan string)
	log.Printf("Starting container")
	go func() { log.Printf("Started container [ID=%s]\n", <-idchan) }()
	err = docker.RunServer(ctx, "rebuilder", 8080, &docker.RunOptions{ID: idchan, Output: log.Writer()})
	if err != nil {
		log.Fatal("Error running rebuilder: ", err.Error())
	}
}
