// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuilder

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"github.com/google/oss-rebuild/build/container"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/benchmark/run"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
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
	URL    *url.URL
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
	rblog := log.New(log.Default().Writer(), fmt.Sprintf("[%-9s]", "rebuilder"), 0)
	go func() {
		in.state = building
		err := container.Build(ctx, "rebuilder")
		if err != nil {
			rblog.Println("Error building container: ", err.Error())
			in.state = dead
			return
		}
		in.state = running
		idchan := make(chan string)
		go func() {
			assetDir := localfiles.AssetsPath()
			err = docker.RunServer(
				ctx,
				"rebuilder",
				8080,
				&docker.RunOptions{
					ID:     idchan,
					Output: logWriter(rblog),
					Mounts: []string{fmt.Sprintf("%s:%s", assetDir, assetDir)},
					Args:   []string{"--debug-storage=file://" + assetDir},
				},
			)
			if err != nil {
				rblog.Println("Error running rebuilder: ", err.Error())
				in.state = dead
				return
			}
		}()
		in.ID = <-idchan
		in.URL = urlx.MustParse("http://localhost:8080")
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
		log.Println("Killing the existing rebuilder")
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

type RunLocalOpts struct {
	Strategy *schema.StrategyOneOf
}

// RunLocal runs the rebuilder for the given example.
func (rb *Rebuilder) RunLocal(ctx context.Context, r rundex.Rebuild, opts RunLocalOpts) {
	inst, err := rb.runningInstance(ctx)
	if err != nil {
		log.Println(errors.Wrap(err, "getting running instance"))
		return
	}
	u := inst.URL.JoinPath("smoketest")
	log.Println("Requesting a smoketest from: " + u.String())
	stub := api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](http.DefaultClient, u)
	// TODO: Should this use benchmark.RunBench?
	resp, err := stub(ctx, schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(r.Ecosystem),
		Package:   r.Package,
		Versions:  []string{r.Version},
		ID:        time.Now().UTC().Format(time.RFC3339),
		Strategy:  opts.Strategy,
	})
	if err != nil {
		log.Println(err.Error())
		return
	}
	msg := "FAILED"
	if len(resp.Verdicts) == 1 && resp.Verdicts[0].Message == "" {
		msg = "SUCCESS"
	}
	log.Printf("Smoketest %s:\n%v", msg, resp)
}

// RunBench executes the benchmark against the local rebuilder.
func (rb *Rebuilder) RunBench(ctx context.Context, set benchmark.PackageSet, runID string) (<-chan schema.Verdict, error) {
	inst, err := rb.runningInstance(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "getting running instance")
	}
	return run.RunBench(ctx, set, run.RunBenchOpts{
		ExecService:    run.NewRemoteExecutionService(http.DefaultClient, inst.URL),
		Mode:           schema.SmoketestMode,
		RunID:          runID,
		MaxConcurrency: 1,
	})
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
