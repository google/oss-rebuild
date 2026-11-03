// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"fmt"
	"html/template"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// pageFuncs are the display formatters every page template can call.
var pageFuncs = template.FuncMap{
	"num":     groupDigits,
	"ago":     func(t time.Time) string { return since(time.Now(), t) },
	"stage":   failedStage,
	"cause":   failureCause,
	"variant": artifactVariant,
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

// failedStage names the stage an attempt failed in, read from the wrapping
// RebuildPackage puts on its message: infer, build or compare. Empty for a
// message without it.
func failedStage(msg string) string {
	switch {
	case strings.HasPrefix(msg, "getting strategy: "):
		return "infer"
	case strings.Contains(msg, "comparing artifacts: "), strings.Contains(msg, "rebuild content mismatch"):
		return "compare"
	case strings.HasPrefix(msg, "executing rebuild: "):
		return "build"
	}
	return ""
}

// failureWrappers are the prefixes the services add to a failure on its way
// into an attempt's message or a session's summary. They read the same on
// every failure.
var failureWrappers = []string{
	"getting strategy: ",
	"fetching inference: ",
	"failed to infer strategy: ",
	"executing rebuild: ",
	"Inference failed: ",
	"generating strategy: ",
	"AI-assisted inference failed: ",
}

var grpcFraming = regexp.MustCompile(`rpc error: code = \w+ desc = `)

// failureCause is a failure message without its gRPC framing and leading
// wrappers, the part that differs between failures.
func failureCause(msg string) string {
	msg = grpcFraming.ReplaceAllString(msg, "")
	for {
		i := slices.IndexFunc(failureWrappers, func(w string) bool { return strings.HasPrefix(msg, w) })
		if i < 0 {
			return msg
		}
		msg = msg[len(failureWrappers[i]):]
	}
}

// artifactVariant is the part of an artifact's name between its version and
// its extension, which tells apart a version's artifacts such as wheels by
// their tags. Empty for a version's usual single artifact, and the whole name
// when it does not carry the version.
func artifactVariant(artifact, version string) string {
	_, rest, ok := strings.Cut(artifact, "-"+version)
	if !ok {
		return artifact
	}
	if strings.HasPrefix(rest, ".") {
		return ""
	}
	rest = strings.TrimPrefix(rest, "-")
	return strings.TrimSuffix(rest, path.Ext(rest))
}
