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
	"pypi.org":   consumeEvery(time.Second),
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
