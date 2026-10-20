// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

// VersionsFile reads the compact index's /versions file. The file lists every
// gem as of its created_at, and each later push or yank appends a line naming
// the gem and the versions it added or removed, so a reader that keeps the
// size it last saw can read just what was appended since. RubyGems rewrites
// the file from scratch, so far at the start of each month, with a new
// created_at.
type VersionsFile struct {
	Client httpx.BasicClient
}

// Appended is one line added to /versions after it was created.
type Appended struct {
	Name string
	// Versions are what the push added. A version built for a platform ends
	// in "-<platform>", and one a yank removed starts with "-".
	Versions []string
}

// headBytes covers the created_at line at the start of the file.
const headBytes = 128

// Head returns the file's created_at and its size.
func (f VersionsFile) Head(ctx context.Context) (createdAt string, size int64, err error) {
	body, total, err := f.read(ctx, 0, headBytes-1)
	if err != nil {
		return "", 0, err
	}
	line, _, _ := bytes.Cut(body, []byte("\n"))
	createdAt, ok := strings.CutPrefix(string(line), "created_at: ")
	if !ok {
		return "", 0, errors.Errorf("file starts with %q, not created_at", line)
	}
	return createdAt, total, nil
}

// Tail returns the lines between offset and end, at most a size Head
// reported, and the offset after the last complete line.
func (f VersionsFile) Tail(ctx context.Context, offset, end int64) ([]Appended, int64, error) {
	if end <= offset {
		return nil, offset, nil
	}
	body, _, err := f.read(ctx, offset, end-1)
	if err != nil {
		return nil, offset, err
	}
	complete := bytes.LastIndexByte(body, '\n') + 1
	var out []Appended
	for _, line := range strings.Split(string(body[:complete]), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, offset, errors.Errorf("line %q has %d fields, want 3", line, len(fields))
		}
		out = append(out, Appended{Name: fields[0], Versions: strings.Split(fields[1], ",")})
	}
	return out, offset + int64(complete), nil
}

// read fetches bytes first through last and the file's total size. Asked
// for a range past the end, rubygems.org answers with the whole file, so a
// response that is not partial is refused unread.
func (f VersionsFile) read(ctx context.Context, first, last int64) ([]byte, int64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.JoinPath("versions").String(), nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", first, last))
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, 0, errors.Errorf("reading /versions bytes %d-%d: %s", first, last, resp.Status)
	}
	_, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
	size, err := strconv.ParseInt(total, 10, 64)
	if !ok || err != nil {
		return nil, 0, errors.Errorf("unreadable Content-Range %q", resp.Header.Get("Content-Range"))
	}
	body, err := io.ReadAll(resp.Body)
	return body, size, err
}
