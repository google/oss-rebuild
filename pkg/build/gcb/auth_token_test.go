// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func TestAuthTokenUsesRawJQOutput(t *testing.T) {
	tests := []struct {
		name string
		tpl  *template.Template
		args map[string]any
	}{
		{"standard build", gcbStandardBuildTpl, map[string]any{"TimewarpAuth": true}},
		{"proxy build", gcbProxyBuildTpl, map[string]any{"ProxyAuth": true}},
		{"asset upload", gcbAssetUploadTpl, map[string]any{"GSUtilAuth": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.tpl.Execute(&out, tt.args); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "| jq -r .access_token > /tmp/token") {
				t.Fatalf("rendered script does not extract the access token as raw text:\n%s", out.String())
			}
		})
	}
}
