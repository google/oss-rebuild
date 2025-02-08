// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// gateway provides a simple HTTP server that redirects to the provided URI applying the configured policy.
//
// Currently, the policy implements a global rate-limit by hostname.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/url"
	"time"
)

func consumeEvery(d time.Duration) chan<- func() {
	var q = make(chan func(), 300)
	go func() {
		for range time.Tick(d) {
			(<-q)()
		}
	}()
	return q
}

func waitOn(c chan<- func()) {
	called := make(chan any)
	c <- func() { called <- nil }
	<-called
	return
}

var queues = map[string]chan<- func(){
	"crates.io":  consumeEvery(time.Second),
	"github.com": consumeEvery(200 * time.Millisecond),
	"pypi.org":   consumeEvery(200 * time.Millisecond),
}

// Handle provides a redirect to the "uri" param applying the configured policy.
func Handle(rw http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(rw, "Bad form data", 400)
		return
	}
	uri := req.Form.Get("uri")
	if uri == "" {
		http.Error(rw, "Empty URI", 400)
		return
	}
	u, err := url.Parse(uri)
	if err != nil {
		http.Error(rw, "Bad URI", 400)
		return
	}
	if c, ok := queues[u.Hostname()]; ok {
		waitOn(c)
	}
	http.Redirect(rw, req, uri, http.StatusFound)
	return
}

func main() {
	flag.Parse()
	http.HandleFunc("/", Handle)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
