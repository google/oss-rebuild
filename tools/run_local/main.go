// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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
