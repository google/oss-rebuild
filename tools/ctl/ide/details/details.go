// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package details

import (
	"bytes"

	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

const (
	defaultBackground = tcell.ColorGray
)

func Format(example rundex.Rebuild) (string, error) {
	type deets struct {
		Success  bool
		Message  string
		Timings  rebuild.Timings
		Strategy schema.StrategyOneOf
	}
	detailsYaml := new(bytes.Buffer)
	enc := yaml.NewEncoder(detailsYaml)
	enc.SetIndent(2)
	err := enc.Encode(deets{
		Success:  example.Success,
		Message:  example.Message,
		Timings:  example.Timings,
		Strategy: example.Strategy,
	})
	if err != nil {
		return "", errors.Wrap(err, "marshalling details")
	}
	return detailsYaml.String(), nil
}

func View(example rundex.Rebuild) (modal.InputCaptureable, error) {
	details := tview.NewTextView()
	text, err := Format(example)
	if err != nil {
		return nil, err
	}
	details.SetText(text).SetBackgroundColor(defaultBackground).SetTitle("Execution details")
	return details, nil
}
