// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scratch provides ctl commands to manually manage scratch build
// VMs through the agent-api broker: mint one (start), run commands on it
// (exec), and tear it down (kill). These are debugging conveniences around
// the broker's /scratch/* routes; the broker still owns VM lifecycle,
// health checking, and idle reaping.
package scratch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/oauth"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Command returns the scratch parent command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scratch",
		Short: "Manually manage scratch build VMs via the agent API",
		Long: "Manually mint a scratch build VM, run commands on it, and tear it down.\n\n" +
			"These wrap the agent-api broker's /scratch/* routes. The broker must be\n" +
			"running with --scratch-enabled. Point --api at its endpoint (Cloud Run\n" +
			"*.run.app hosts are authenticated automatically with your user credentials).",
	}
	cmd.AddCommand(startCommand())
	cmd.AddCommand(execCommand())
	cmd.AddCommand(killCommand())
	return cmd
}

// dialAPI parses the broker API endpoint and returns an HTTP client suited
// to it: an ID-token-authorized client for Cloud Run (*.run.app) hosts, or
// the default client otherwise. Mirrors the auth selection in run-agent.
func dialAPI(ctx context.Context, apiRaw string) (*http.Client, *url.URL, error) {
	u, err := url.Parse(apiRaw)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parsing API endpoint")
	}
	if strings.Contains(u.Host, "run.app") {
		u.Scheme = "https"
		client, err := oauth.AuthorizedUserIDClient(ctx)
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating authorized HTTP client")
		}
		return client, u, nil
	}
	return http.DefaultClient, u, nil
}

// parseGCSURI splits a gs://bucket/object URI into its bucket and object.
func parseGCSURI(uri string) (bucket, object string, err error) {
	rest, ok := strings.CutPrefix(uri, "gs://")
	if !ok {
		return "", "", errors.Errorf("malformed gs:// URI %q", uri)
	}
	bucket, object, ok = strings.Cut(rest, "/")
	if !ok || bucket == "" || object == "" {
		return "", "", errors.Errorf("malformed gs:// URI %q", uri)
	}
	return bucket, object, nil
}

// tailGCSObject streams the bytes of the object at a gs:// URI from offset
// to its current end into w, returning the number of bytes written. It
// stats before reading so a missing object (no output captured yet) or an
// unchanged one (no new bytes since the last poll) yields 0 bytes and no
// error rather than a range error.
func tailGCSObject(ctx context.Context, gcs *storage.Client, uri string, offset int64, w io.Writer) (int64, error) {
	bucket, object, err := parseGCSURI(uri)
	if err != nil {
		return 0, err
	}
	obj := gcs.Bucket(bucket).Object(object)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return 0, nil
		}
		return 0, errors.Wrap(err, "stat object")
	}
	if attrs.Size <= offset {
		return 0, nil
	}
	r, err := obj.NewRangeReader(ctx, offset, -1)
	if err != nil {
		return 0, errors.Wrap(err, "opening object")
	}
	defer r.Close()
	n, err := io.Copy(w, r)
	if err != nil {
		return n, errors.Wrap(err, "reading object")
	}
	return n, nil
}
