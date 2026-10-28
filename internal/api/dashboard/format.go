// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"fmt"
	"html/template"
	"strconv"
	"time"
)

// pageFuncs are the display formatters every page template can call.
var pageFuncs = template.FuncMap{
	"num": groupDigits,
	"ago": func(t time.Time) string { return since(time.Now(), t) },
}

// groupDigits renders n with comma thousands separators: 14889 as 14,889.
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	lead := 1
	if n < 0 {
		lead = 2
	}
	for i := len(s) - 3; i >= lead; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// since renders how long before now t was in its largest whole unit, and
// past a month the date instead.
func since(now, t time.Time) string {
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", d/time.Minute)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", d/time.Hour)
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", d/(24*time.Hour))
	default:
		return t.Format("2006-01-02")
	}
}
