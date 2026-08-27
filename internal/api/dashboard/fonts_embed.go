// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build webfonts

package dashboard

import (
	_ "embed"
	"net/http"
)

// The font binaries are fetched, not committed: run
// `go generate ./internal/api/dashboard` once per checkout before building
// with -tags webfonts. fonts/fetch_fonts.go pins their digests.
var (
	//go:embed fonts/public-sans-latin.woff2
	publicSansWoff2 []byte
	//go:embed fonts/merriweather-700-latin.woff2
	merriweatherWoff2 []byte
)

// fontsHTML fills the header's webfonts marker so every page preloads the
// embedded faces.
const fontsHTML = `    <link rel="preload" href="/fonts/public-sans-latin.woff2" as="font" type="font/woff2" crossorigin>
    <link rel="preload" href="/fonts/merriweather-700-latin.woff2" as="font" type="font/woff2" crossorigin>
`

// fontsCSS declares the embedded latin subsets. font-display: fallback
// gives a ~100ms block period, which a same-origin preloaded font beats, so
// pages paint with the face in place instead of swapping it in.
const fontsCSS = `@font-face {
    font-family: 'Public Sans';
    font-style: normal;
    font-weight: 100 900;
    font-display: fallback;
    src: url('/fonts/public-sans-latin.woff2') format('woff2');
    unicode-range: U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD;
}
@font-face {
    font-family: 'Merriweather';
    font-style: normal;
    font-weight: 700;
    font-display: fallback;
    src: url('/fonts/merriweather-700-latin.woff2') format('woff2');
    unicode-range: U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD;
}

`

// registerFonts serves the embedded faces. Fonts are immutable: a face
// change ships under a new path.
func registerFonts(mux *http.ServeMux) {
	for path, content := range map[string][]byte{
		"/fonts/public-sans-latin.woff2":      publicSansWoff2,
		"/fonts/merriweather-700-latin.woff2": merriweatherWoff2,
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = w.Write(content)
		})
	}
}
