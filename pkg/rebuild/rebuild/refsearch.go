// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"io"
	"time"

	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

var (
	ErrNoRef      = errors.New("no git ref")
	ErrNoValidRef = errors.New("no git ref: all candidates invalid")
)

// ErrorWithFallbackRef turns an ErrNoRef or ErrNoValidRef into an InferenceError
// whose location is HEAD's last commit by the publish time, so a caller can
// look for the version's commit from there. Other errors pass through.
func ErrorWithFallbackRef(err error, repo *git2.Repository, loc Location, published time.Time) error {
	if !errors.Is(err, ErrNoRef) && !errors.Is(err, ErrNoValidRef) {
		return err
	}
	ref, rerr := fallbackRef(repo, published)
	if rerr != nil {
		return err
	}
	loc.Ref = ref
	return &InferenceError{
		Detail: InferenceErrorDetail{Kind: FailureNoRef, Location: loc, Published: published},
		Err:    errors.Wrap(err, "defaulting to the last commit by the publish time"),
	}
}

// fallbackRef returns the last commit on HEAD's history made by the publish
// time, else HEAD. A build can't use a commit made after publish.
func fallbackRef(repo *git2.Repository, published time.Time) (string, error) {
	head, err := repo.Head()
	if err != nil {
		return "", errors.Wrap(err, "resolving HEAD")
	}
	if published.IsZero() {
		return head.Hash().String(), nil
	}
	it, err := repo.Log(&git2.LogOptions{From: head.Hash(), Order: git2.LogOrderCommitterTime, Until: &published})
	if err != nil {
		return "", errors.Wrap(err, "walking HEAD")
	}
	defer it.Close()
	c, err := it.Next()
	if err == io.EOF {
		return head.Hash().String(), nil
	} else if err != nil {
		return "", errors.Wrap(err, "walking HEAD")
	}
	return c.Hash.String(), nil
}
