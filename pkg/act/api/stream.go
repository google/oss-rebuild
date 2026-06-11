// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"iter"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// StreamHandlerFunc is the server-side type of a server-streaming action.
// Mirror of HandlerFunc for streams.
type StreamHandlerFunc[I act.Input, E any, D act.Deps] func(context.Context, I, D) iter.Seq2[*E, error]

// StreamStubFunc is a client-side harness for that issues one
// streaming request and returns an iterator over events.
type StreamStubFunc[I act.Input, E any] func(context.Context, I) iter.Seq2[*E, error]

// Streaming wire format (internal): the response is a text/event-stream
// with three event names:
//
//	event: data   JSON of *E. One per produced event.
//	event: error  protojson google.rpc.Status. Final event on failure.
//	event: done   Terminator on success. Empty data body.
const (
	sseEventData  = "data"
	sseEventError = "error"
	sseEventDone  = "done"
)

// maxSSELineBytes bounds a single SSE line on both sides of the wire: the
// client's scanner refuses longer lines, and the server emits an in-band
// error instead of a data event that would exceed it.
const maxSSELineBytes = 16 << 20

// StreamHandler is the streaming counterpart to Handler. The handler
// returns an iter.Seq2[*E, error]; the framework drives the wire.
func StreamHandler[I act.Input, E any, D act.Deps](initDeps InitDeps[D], handler StreamHandlerFunc[I, E, D]) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		// Streaming uses the request context so client disconnects propagate
		// to the producer (unlike the unary handler, which detaches).
		ctx := r.Context()
		req, ok := decodeRequest[I](rw, r)
		if !ok {
			return
		}
		deps, err := initDeps(ctx)
		if err != nil {
			log.Println(errors.Wrap(err, "initializing dependencies"))
			http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		seq := handler(ctx, req, deps)
		if seq == nil {
			// iter.Pull2 panics on a nil seq; treat it as an empty stream.
			seq = func(func(*E, error) bool) {}
		}
		next, stop := iter.Pull2(seq)
		defer stop()

		// Peek the first pull. An error here is a pre-stream failure
		// mapped to a unary HTTP error response so HTTP-aware infra
		// observes the proper status code; any accompanying frame is
		// dropped, matching in-stream precedence. Later errors go in-band.
		frame, err, ok := next()
		if ok && err != nil {
			writeStatusError(rw, err)
			return
		}
		writeStreamHeaders(rw)
		// NOTE: Wrapping rw without http.Flusher silently disables streaming.
		flusher, _ := rw.(http.Flusher)
		for ; ok; frame, err, ok = next() {
			if err != nil {
				emitErrorEvent(rw, flusher, err)
				return
			}
			if !emitDataEvent(rw, flusher, frame) {
				return
			}
			if err := ctx.Err(); err != nil {
				// Client gone or server shut down. Don't bother with a
				// terminator; the consumer can't read it anyway.
				log.Printf("context error: %s", err.Error())
				return
			}
		}
		writeSSEEvent(rw, sseEventDone, "{}")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// StreamStub is the streaming counterpart to Stub. Each call issues one
// request and returns an iterator that yields events as they arrive.
// Breaking out of the range cancels the in-flight HTTP request.
func StreamStub[I act.Input, E any](client httpx.BasicClient, u *url.URL) StreamStubFunc[I, E] {
	return func(ctx context.Context, in I) iter.Seq2[*E, error] {
		return func(yield func(*E, error) bool) {
			reqCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			req, err := newFormRequest(reqCtx, u, in)
			if err != nil {
				yield(nil, err)
				return
			}
			req.Header.Set("Accept", "text/event-stream")
			resp, err := client.Do(req)
			if err != nil {
				yield(nil, errors.Wrap(err, "making http request"))
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				yield(nil, errorFromResponse(resp))
				return
			}
			drainSSE(resp.Body, yield)
		}
	}
}

// drainSSE reads SSE events from r and feeds them to yield. Returns when
// the stream terminates (done event, error event, EOF, or yield returns
// false). EOF without done surfaces as ErrUnavailable. Callers that
// want to retry resume from whatever offset their domain tracks.
func drainSSE[E any](r io.Reader, yield func(*E, error) bool) {
	sc := bufio.NewScanner(r)
	// The ceiling fits a 64 KiB binary chunk encoded as base64-in-JSON
	// (~90 KiB) with generous headroom.
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELineBytes)

	var (
		eventName string
		dataBuf   strings.Builder
	)

	// Yields the accumulated event. Returns false when reading should stop.
	flush := func() bool {
		defer func() {
			eventName = ""
			dataBuf.Reset()
		}()
		switch eventName {
		case sseEventData:
			var e E
			if err := json.Unmarshal([]byte(dataBuf.String()), &e); err != nil {
				yield(nil, errors.Wrap(err, "decoding event"))
				return false
			}
			return yield(&e, nil)
		case sseEventError:
			if st, ok := readStatusFromBody([]byte(dataBuf.String())); ok && st.Code() != codes.OK {
				yield(nil, st.Err())
			} else {
				yield(nil, errors.New("server error"))
			}
			return false
		case sseEventDone:
			return false
		default:
			// Unknown / missing event name. Ignore for forward-compatibility.
			return true
		}
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment frame (used for keepalives). Ignore.
			continue
		}
		field, val := splitSSEField(line)
		switch field {
		case "event":
			eventName = val
		case "data":
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(val)
		}
		// Other fields (id, retry) are accepted but ignored at this layer.
	}
	// Only reachable when the stream cuts off: every terminal event (done,
	// error, consumer break) returns from inside the loop.
	if err := sc.Err(); err != nil {
		yield(nil, errors.Wrap(err, "reading stream"))
		return
	}
	yield(nil, ErrUnavailable)
}

func splitSSEField(line string) (field, value string) {
	field, value, ok := strings.Cut(line, ":")
	if !ok {
		return line, ""
	}
	// SSE spec: a single leading space after the colon is stripped.
	return field, strings.TrimPrefix(value, " ")
}

// writeStreamHeaders sets the SSE response headers and flushes them so the
// client sees the stream has opened.
func writeStreamHeaders(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.WriteHeader(http.StatusOK)
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSSEEvent(w io.Writer, name, data string) {
	_, _ = io.WriteString(w, "event: "+name+"\n")
	// Split on '\n' per the SSE spec. JSON payloads from encoding/json
	// never contain literal newlines, so this loop is single-iteration
	// in practice.
	for l := range strings.SplitSeq(data, "\n") {
		_, _ = io.WriteString(w, "data: "+l+"\n")
	}
	_, _ = io.WriteString(w, "\n")
}

// emitDataEvent JSON-encodes frame, writes one SSE data event, and flushes.
// Nil frames are skipped, mirroring the unary handler's nil-output handling.
// Unencodable or oversized frames terminate the stream with an in-band
// error. Returns false when the caller should stop iterating.
func emitDataEvent[E any](w io.Writer, flusher http.Flusher, frame *E) bool {
	if frame == nil {
		return true
	}
	body, err := json.Marshal(frame)
	if err != nil {
		emitErrorEvent(w, flusher, AsStatus(codes.Internal, errors.Wrap(err, "encoding event")))
		return false
	}
	if len(body)+len("data: ") > maxSSELineBytes {
		emitErrorEvent(w, flusher, AsStatus(codes.Internal, errors.Errorf("%d-byte event exceeds stream maximum", len(body))))
		return false
	}
	writeSSEEvent(w, sseEventData, string(body))
	if flusher != nil {
		flusher.Flush()
	}
	return true
}

// emitErrorEvent writes the in-band error terminator and flushes.
func emitErrorEvent(w io.Writer, flusher http.Flusher, err error) {
	log.Println(err)
	writeSSEEvent(w, sseEventError, statusBodyFor(err))
	if flusher != nil {
		flusher.Flush()
	}
}
