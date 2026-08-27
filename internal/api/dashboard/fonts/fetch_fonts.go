// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// This program fetches the dashboard's font files into this directory.
// The woff2 binaries stay out of version control: the pinned URLs and
// SHA-256 digests below are the source of truth, and a download that does
// not match its digest is discarded.
//
// Both faces are licensed under the SIL Open Font License 1.1:
// https://github.com/google/fonts/blob/main/ofl/publicsans/OFL.txt
// https://github.com/google/fonts/blob/main/ofl/merriweather/OFL.txt
//
// Run: go generate ./internal/api/dashboard
//
// To move a pin (new font version or added face): request the css2 URL in
// the header with a browser User-Agent, take the woff2 URL from the latin
// @font-face block, and record its SHA-256, e.g.
//
//	curl -sA Mozilla/5.0 'https://fonts.googleapis.com/css2?family=Public+Sans:wght@400..700&display=swap'
//	curl -s <woff2 url> | shasum -a 256
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

var fonts = []struct{ name, url, sha256 string }{
	{
		"public-sans-latin.woff2",
		"https://fonts.gstatic.com/s/publicsans/v21/ijwRs572Xtc6ZYQws9YVwnNGfJ7QwOk1.woff2",
		"c1b6da516e0062e9c2f341b3a51dd2d621d946da72f06c6cfe05fd9d2dd8622d",
	},
	{
		"merriweather-700-latin.woff2",
		"https://fonts.gstatic.com/s/merriweather/v33/u-4D0qyriQwlOrhSvowK_l5UcA6zuSYEqOzpPe3HOZJ5eX1WtLaQwmYiScCmDxhtNOKl8yDrOSAaFF31CPDaYKfF.woff2",
		"a7e8678fe035f7e2f7e3ffed8915cb234713b19f61f76a65d1bc74223da1db2e",
	},
}

func main() {
	for _, f := range fonts {
		body, err := fetch(f.url)
		if err != nil {
			log.Fatalf("fetching %s: %v", f.name, err)
		}
		if got := sha256.Sum256(body); hex.EncodeToString(got[:]) != f.sha256 {
			log.Fatalf("%s: digest %x does not match pinned %s", f.name, got, f.sha256)
		}
		path := filepath.Join("fonts", f.name)
		if err := os.WriteFile(path, body, 0644); err != nil {
			log.Fatalf("writing %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(body))
	}
}

func fetch(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
