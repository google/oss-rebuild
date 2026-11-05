// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

// Change is one event in PyPI's changelog: a project or release created or
// removed, a file uploaded or removed, or an ownership change.
type Change struct {
	Project string // as the project spells its name
	Version string // empty for project-level events
	Time    time.Time
	Action  string // such as "new release" or "add py3 file foo-1.0-py3-none-any.whl"
	Serial  int64
}

// UploadedFile returns the file an "add <python> file <name>" event uploaded.
func (c Change) UploadedFile() (string, bool) {
	fields := strings.Fields(c.Action)
	if len(fields) != 4 || fields[0] != "add" || fields[2] != "file" {
		return "", false
	}
	return fields[3], true
}

// Changelog reads PyPI's changelog over its XML-RPC API, which mirrors use to
// follow the index. Each event carries a serial one greater than the last.
type Changelog struct {
	Client httpx.BasicClient
}

// LastSerial returns the serial of the newest event.
func (c Changelog) LastSerial(ctx context.Context) (int64, error) {
	var serial int64
	err := c.call(ctx, "<methodName>changelog_last_serial</methodName><params/>", func(v rpcValue) error {
		var ok bool
		serial, ok = v.int()
		if !ok {
			return errors.New("serial is not an int")
		}
		return nil
	})
	return serial, err
}

// Since returns up to 50,000 events after serial, oldest first, the most PyPI
// returns in one call.
func (c Changelog) Since(ctx context.Context, serial int64) ([]Change, error) {
	method := fmt.Sprintf("<methodName>changelog_since_serial</methodName><params><param><value><int>%d</int></value></param></params>", serial)
	var out []Change
	err := c.call(ctx, method, func(v rpcValue) error {
		for i, e := range v.Array {
			if len(e.Array) != 5 {
				return errors.Errorf("event %d has %d fields, want 5", i, len(e.Array))
			}
			at, ok := e.Array[2].int()
			if !ok {
				return errors.Errorf("event %d has no timestamp", i)
			}
			s, ok := e.Array[4].int()
			if !ok {
				return errors.Errorf("event %d has no serial", i)
			}
			out = append(out, Change{
				Project: e.Array[0].string(),
				Version: e.Array[1].string(),
				Time:    time.Unix(at, 0).UTC(),
				Action:  e.Array[3].string(),
				Serial:  s,
			})
		}
		return nil
	})
	return out, err
}

func (c Changelog) call(ctx context.Context, method string, decode func(rpcValue) error) error {
	body := `<?xml version="1.0"?><methodCall>` + method + `</methodCall>`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, registryURL.JoinPath("pypi").String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.Wrap(errors.New(resp.Status), "calling the changelog")
	}
	var r struct {
		Params []rpcValue `xml:"params>param>value"`
		Fault  *rpcValue  `xml:"fault>value"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&r); err != nil {
		return errors.Wrap(err, "decoding the changelog response")
	}
	if r.Fault != nil {
		return errors.Errorf("changelog fault: %s", r.Fault.member("faultString").string())
	}
	if len(r.Params) != 1 {
		return errors.Errorf("changelog returned %d values, want 1", len(r.Params))
	}
	return decode(r.Params[0])
}

// rpcValue is an XML-RPC value. A value with no type element is a string.
type rpcValue struct {
	String  *string    `xml:"string"`
	Int     *int64     `xml:"int"`
	I4      *int64     `xml:"i4"`
	Array   []rpcValue `xml:"array>data>value"`
	Members []struct {
		Name  string   `xml:"name"`
		Value rpcValue `xml:"value"`
	} `xml:"struct>member"`
	Text string `xml:",chardata"`
}

func (v rpcValue) member(name string) rpcValue {
	for _, m := range v.Members {
		if m.Name == name {
			return m.Value
		}
	}
	return rpcValue{}
}

func (v rpcValue) string() string {
	if v.String != nil {
		return *v.String
	}
	return strings.TrimSpace(v.Text)
}

func (v rpcValue) int() (int64, bool) {
	switch {
	case v.Int != nil:
		return *v.Int, true
	case v.I4 != nil:
		return *v.I4, true
	}
	return 0, false
}
