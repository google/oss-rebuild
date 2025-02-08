// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/pkg/errors"
)

// Attestor is a verifier that signs and publishes attestation bundles.
type Attestor struct {
	Store          rebuild.AssetStore
	Signer         InTotoEnvelopeSigner
	AllowOverwrite bool
}

// BundleExists returns whether an existing attestation bundle exists.
func (a Attestor) BundleExists(ctx context.Context, t rebuild.Target) (bool, error) {
	r, err := a.Store.Reader(ctx, rebuild.AttestationBundleAsset.For(t))
	if errors.Is(err, rebuild.ErrAssetNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	} else {
		defer r.Close()
		return true, nil
	}
}

// PublishBundle signs and publishes an attestation bundle.
func (a Attestor) PublishBundle(ctx context.Context, t rebuild.Target, stmts ...*in_toto.ProvenanceStatementSLSA1) error {
	if exists, err := a.BundleExists(ctx, t); err != nil {
		return errors.Wrap(err, "checking for existing bundle")
	} else if exists && !a.AllowOverwrite {
		return errors.New("bundle already exists")
	}
	bundle := bytes.NewBuffer(nil)
	e := json.NewEncoder(bundle)
	for _, stmt := range stmts {
		envelope, err := a.Signer.SignStatement(ctx, stmt)
		if err != nil {
			return errors.Wrap(err, "signing attestation")
		}
		if err := e.Encode(envelope); err != nil {
			return errors.Wrap(err, "marshalling DSSE")
		}
	}
	w, err := a.Store.Writer(ctx, rebuild.AttestationBundleAsset.For(t))
	if err != nil {
		return errors.Wrap(err, "creating writer for bundle")
	}
	if _, err := io.Copy(w, bundle); err != nil {
		return errors.Wrap(err, "uploading bundle")
	}
	if err := w.Close(); err != nil {
		return errors.Wrap(err, "closing bundle upload")
	}
	return nil
}
