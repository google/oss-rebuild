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

package ide

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/build/binary"
	"github.com/google/oss-rebuild/build/container"
	"github.com/google/oss-rebuild/tools/ctl/firestore"
	"github.com/google/oss-rebuild/tools/docker"
	"github.com/pkg/errors"
)

func logWriter(dest *log.Logger) io.Writer {
	pr, pw := io.Pipe()
	br := bufio.NewReader(pr)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			dest.Output(1, line)
		}
	}()
	return pw
}

type instanceState int

const (
	created instanceState = iota
	starting
	building
	running
	serving
	dead
)

// Instance represents a single run of the rebuilder container.
type Instance struct {
	ID     string
	cancel func()
	state  instanceState
}

// Run triggers the startup of the Instance.
// Should only be called once to initialize an Instance.
func (in *Instance) Run(ctx context.Context) {
	if in.state != created {
		return
	}
	in.state = starting
	ctx, in.cancel = context.WithCancel(ctx)
	// Make the rebuilder write out to the log widget with a [rebuilder] prefix.
	rblog := log.New(log.Default().Writer(), logPrefix("rebuilder"), 0)
	go func() {
		in.state = building
		path, err := binary.Build(ctx, "rebuilder")
		if err != nil {
			rblog.Println("Error building binary: ", err.Error())
			in.state = dead
			return
		}
		err = container.Build(ctx, "rebuilder", path)
		if err != nil {
			rblog.Println("Error building container: ", err.Error())
			in.state = dead
			return
		}
		in.state = running
		idchan := make(chan string)
		go func() {
			err = docker.RunServer(ctx, "rebuilder", 8080, &docker.RunOptions{ID: idchan, Output: logWriter(rblog)})
			if err != nil {
				rblog.Println("Error running rebuilder: ", err.Error())
				in.state = dead
				return
			}
		}()
		in.ID = <-idchan
		if in.ID != "" {
			in.state = serving
		}
	}()
}

// Kill does a non-blocking shutdown of the Instance container.
func (in *Instance) Kill() {
	in.cancel()
	in.state = dead
}

// Serving returns whether the Instance is serving.
func (in *Instance) Serving() bool {
	return in.state == serving
}

// Dead returns whether the Instance is dead.
func (in *Instance) Dead() bool {
	return in.state == dead
}

// Wait provides a way of getting alerted when the Instance initialization is complete.
func (in *Instance) Wait(ctx context.Context) <-chan error {
	out := make(chan error)
	go func() {
		for !in.Dead() && !in.Serving() {
			select {
			case <-ctx.Done():
				out <- ctx.Err()
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		if in.Dead() {
			out <- errors.New("instance creation failed")
		} else {
			out <- nil
		}
	}()
	return out
}

// Rebuilder manages a local instance of the rebuilder docker container.
type Rebuilder struct {
	instance *Instance
	m        sync.Mutex
}

// Kill does a non-blocking shutdown of the rebuilder container.
func (rb *Rebuilder) Kill() {
	rb.m.Lock()
	defer rb.m.Unlock()
	if rb.instance != nil && !rb.instance.Dead() {
		log.Println("Killing the exisitng rebuilder")
		rb.instance.Kill()
		rb.instance = nil
		log.Printf("rebuilder exited")
	}
}

// Instance returns the underlying rebuilder instance currently in use.
func (rb *Rebuilder) Instance() *Instance {
	rb.m.Lock()
	defer rb.m.Unlock()
	if rb.instance == nil || rb.instance.Dead() {
		rb.instance = &Instance{}
	}
	return rb.instance
}

func (rb *Rebuilder) runningInstance(ctx context.Context) (*Instance, error) {
	inst := rb.Instance()
	inst.Run(ctx)
	ctxtimeout, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	log.Printf("Waiting for rebuilder to be serving...")
	if err := <-inst.Wait(ctxtimeout); err != nil {
		return nil, err
	}
	log.Printf("Rebuilder is now serving.")
	return inst, nil
}

// Restart restarts the rebuilder container.
func (rb *Rebuilder) Restart(ctx context.Context) {
	rb.Kill()
	log.Println("Starting new local instance of the rebuilder.")
	_, err := rb.runningInstance(ctx)
	if err != nil {
		log.Println(err)
	}
}

// RunLocal runs the rebuilder for the given example.
// Each element of extraParams is a url parameter like "param=value".
func (rb *Rebuilder) RunLocal(ctx context.Context, r firestore.Rebuild, extraParams ...string) {
	_, err := rb.runningInstance(ctx)
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("Calling the rebuilder for %s\n", r.ID())
	id := time.Now().UTC().Format(time.RFC3339)
	var extra string
	if len(extraParams) > 0 {
		extra = "&" + strings.Join(extraParams, "&")
	}
	cmd := exec.CommandContext(ctx, "curl", "--silent", "-d", fmt.Sprintf("ecosystem=%s&pkg=%s&versions=%s&id=%s%s", r.Ecosystem, r.Package, r.Version, id, extra), "-X", "POST", "127.0.0.1:8080/smoketest")
	rllog := logWriter(log.New(log.Default().Writer(), logPrefix("runlocal"), 0))
	cmd.Stdout = rllog
	cmd.Stderr = rllog
	log.Println(cmd.String())
	if err := cmd.Start(); err != nil {
		log.Println(err)
	}
}

// Attach opens a new tmux window that's attached to the rebuilder container.
func (rb *Rebuilder) Attach(ctx context.Context) error {
	inst := rb.Instance()
	if !inst.Serving() {
		return errors.New("rebuilder container not serving")
	}
	cmd := exec.CommandContext(ctx, "tmux", "new-window", fmt.Sprintf("docker exec -it %s sh", inst.ID))
	return cmd.Run()
}
