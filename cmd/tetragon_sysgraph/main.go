// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// tetragon_sysgraph converts tetragon events into a sysgraph .zip archive.
//
// It supports two modes:
//   - Streaming: connects to a running tetragon gRPC server (-server flag)
//   - Batch: reads a tetragon JSONL file (-input flag)
package main

import (
	"archive/zip"
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tetragonpb "github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgir"
	"github.com/google/oss-rebuild/pkg/sysgraph/tetragon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	input := flag.String("input", "", "Path to tetragon JSONL file (batch mode)")
	server := flag.String("server", "", "Tetragon gRPC server address, e.g. localhost:54321 (streaming mode)")
	output := flag.String("output", "", "Path for output sysgraph .zip file")
	graphID := flag.String("graph-id", "build", "Graph ID for the sysgraph")
	flag.Parse()

	if *output == "" || (*input == "" && *server == "") {
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	conv := tetragon.NewConverter()
	// Convert events using BufferedDiskWriter (buffered file I/O, bounded memory).
	var reader sgir.Reader
	irDir, err := os.MkdirTemp("", "sysgraph-ir-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(irDir)
	diskFmt := &sgir.DiskFormat{BasePath: irDir, Format: sgir.PBDelim}
	reader = diskFmt
	bw := sgir.NewBufferedDiskWriter(irDir, sgir.PBDelim)
	if *server != "" {
		// Set up signal handling for graceful teardown.
		//   SIGUSR1: build is done. Record the current timestamp and keep
		//     consuming events until we see one at or after that time.
		//   SIGTERM: stop immediately (fallback).
		var drainAfter atomic.Pointer[time.Time]
		streamCtx, streamCancel := context.WithCancel(ctx)
		defer streamCancel()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			if sig == syscall.SIGUSR1 {
				t := time.Now()
				drainAfter.Store(&t)
				log.Printf("Received SIGUSR1, will drain until events reach %v", t.Format(time.RFC3339))
			} else {
				log.Printf("Received SIGTERM, stopping immediately")
				streamCancel()
			}
		}()
		if err := streamEvents(streamCtx, *server, conv, bw, &drainAfter); err != nil {
			log.Fatalf("Failed to stream events: %v", err)
		}
	} else {
		events, err := parseTetragonJSONL(*input)
		if err != nil {
			log.Fatalf("Failed to parse tetragon JSONL: %v", err)
		}
		log.Printf("Parsed %d tetragon events", len(events))
		if err := conv.Convert(ctx, events, bw); err != nil {
			log.Fatalf("Failed to convert events: %v", err)
		}
	}
	if err := bw.Close(); err != nil {
		log.Fatalf("Failed to close buffered writer: %v", err)
	}
	// Build sysgraph from IR.
	// Use background context since streamCtx may be cancelled.
	sgDir, err := os.MkdirTemp("", "sysgraph-out-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(sgDir)

	builder := &sgir.Builder{
		ConcurrencyLimit: 8,
	}
	if err := builder.ToSysGraph(ctx, *graphID, reader, sgDir); err != nil {
		log.Fatalf("Failed to build sysgraph: %v", err)
	}

	// Zip the output directory.
	if err := zipDir(sgDir, *output); err != nil {
		log.Fatalf("Failed to create zip: %v", err)
	}
	log.Printf("Sysgraph written to %s", *output)
}

func streamEvents(ctx context.Context, addr string, conv *tetragon.Converter, w sgir.Writer, drainAfter *atomic.Pointer[time.Time]) error {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Large flow control windows to avoid gRPC-level backpressure
		// reaching tetragon's eBPF ring buffer.
		grpc.WithInitialWindowSize(32*1024*1024),
		grpc.WithInitialConnWindowSize(32*1024*1024),
	)
	if err != nil {
		return fmt.Errorf("creating gRPC client: %w", err)
	}
	defer conn.Close()

	client := tetragonpb.NewFineGuidanceSensorsClient(conn)

	// Retry the initial stream connection with exponential backoff.
	// Tetragon may not be fully ready when we first try to connect.
	var stream grpc.ServerStreamingClient[tetragonpb.GetEventsResponse]
	backoff := time.Second
	for attempts := 0; ; attempts++ {
		stream, err = client.GetEvents(ctx, &tetragonpb.GetEventsRequest{})
		if err == nil {
			break
		}
		if attempts >= 5 {
			return fmt.Errorf("starting event stream after %d attempts: %w", attempts+1, err)
		}
		log.Printf("Failed to start event stream (attempt %d): %v, retrying in %v", attempts+1, err, backoff)
		time.Sleep(backoff)
		backoff *= 2
	}
	log.Printf("Connected to tetragon event stream at %s", addr)

	// Buffered channel decouples gRPC reads from processing.
	// The reader goroutine drains the gRPC stream as fast as possible
	// into this buffer, preventing backpressure from propagating to
	// tetragon's per-listener channel and causing event drops.
	const eventBufSize = 100_000
	eventCh := make(chan *tetragonpb.GetEventsResponse, eventBufSize)

	var readCount atomic.Int64
	var readerErr error

	// Reader goroutine: pull events off the wire as fast as possible.
	go func() {
		defer close(eventCh)
		for {
			event, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				readerErr = err
				return
			}
			readCount.Add(1)
			eventCh <- event
		}
	}()

	// Convert events directly into the in-memory writer.
	const writeBufSize = 100_000
	writeCh := make(chan *tetragonpb.GetEventsResponse, writeBufSize)
	var writeCount atomic.Int64
	var writerErr error
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for event := range writeCh {
			if err := conv.ConvertEvent(ctx, event, w); err != nil {
				writerErr = fmt.Errorf("converting event %d: %w", writeCount.Load(), err)
				for range writeCh {
				}
				return
			}
			writeCount.Add(1)
		}
	}()

	// Process events from the buffer. If drainAfter is set (SIGUSR1 received),
	// wait until we see an event at or after that timestamp, then continue
	// processing until there is a period of stragglerTimeout duration with no
	// qualifying events received.
	// NOTE: Events may arrive out of order as the independent queues within
	// tetragon are drained simultaneously. We don't have the data necessary to
	// know for sure whether all events we care about have arrived but this
	// straggler approach should effectively approximate it.
	const stragglerTimeout = 5 * time.Second
	var processCount int
	var latestEventTime time.Time
	var stragglerTimer *time.Timer
	var stragglerCh <-chan time.Time // nil until drain target reached
loop:
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				break loop
			}
			processCount++
			var eventTime time.Time
			if t := event.GetTime(); t != nil {
				eventTime = t.AsTime()
				if eventTime.After(latestEventTime) {
					latestEventTime = eventTime
				}
			}
			if processCount%100_000 == 0 {
				buffered := readCount.Load() - int64(processCount)
				log.Printf("Processed %d events (buffered: ~%d, written: %d, latest: %v)...", processCount, buffered, writeCount.Load(), latestEventTime.Format(time.RFC3339))
			}
			writeCh <- event
			target := drainAfter.Load()
			if target == nil {
				continue
			}
			if stragglerTimer == nil && !latestEventTime.IsZero() && !latestEventTime.Before(*target) {
				// First event at or after target -> start the straggler timer.
				stragglerTimer = time.NewTimer(stragglerTimeout)
				stragglerCh = stragglerTimer.C
				log.Printf("Events reached drain target (latest=%v, target=%v), waiting %v for stragglers...",
					latestEventTime.Format(time.RFC3339), target.Format(time.RFC3339), stragglerTimeout)
			} else if stragglerTimer != nil && !eventTime.IsZero() && eventTime.Before(*target) {
				// Got a straggler (event before target) -> reset the timer.
				if !stragglerTimer.Stop() {
					select {
					case <-stragglerTimer.C:
					default:
					}
				}
				stragglerTimer.Reset(stragglerTimeout)
			}
		case <-stragglerCh:
			log.Printf("No stragglers for %v, stopping after %d events", stragglerTimeout, processCount)
			close(writeCh)
			writerWg.Wait()
			if writerErr != nil {
				return writerErr
			}
			log.Printf("Writer finished: %d events written", writeCount.Load())
			return nil
		}
	}
	// Main loop exited. Wait for writer to finish.
	close(writeCh)
	writerWg.Wait()
	if writerErr != nil {
		return writerErr
	}
	if readerErr != nil {
		if ctx.Err() != nil {
			log.Printf("Stream stopped (signal received) after %d read, %d processed", readCount.Load(), processCount)
		} else {
			log.Printf("Stream ended with error after %d read, %d processed: %v", readCount.Load(), processCount, readerErr)
		}
	}
	log.Printf("Processed %d total events, %d written", processCount, writeCount.Load())
	return nil
}

func parseTetragonJSONL(path string) ([]*tetragonpb.GetEventsResponse, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []*tetragonpb.GetEventsResponse
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line size
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		event := &tetragonpb.GetEventsResponse{}
		if err := protojson.Unmarshal(line, event); err != nil {
			// Skip unparseable lines.
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func zipDir(srcDir, destZip string) error {
	f, err := os.Create(destZip)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		zf, err := w.Create(relPath)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = zf.Write(content)
		return err
	})
}
