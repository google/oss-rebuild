// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build !webfonts

package dashboard

import "net/http"

// Without the webfonts tag pages render with the system font stacks and no
// font assets are embedded or served, so a clean checkout builds with a
// plain go build.
const (
	fontsHTML = ""
	fontsCSS  = ""
)

func registerFonts(*http.ServeMux) {}
