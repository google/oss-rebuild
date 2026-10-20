// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestChangelogSince(t *testing.T) {
	body := `<?xml version='1.0'?>
<methodResponse><params><param>
<value><array><data>
<value><array><data>
<value><string>Flit.Core</string></value>
<value><string>3.12.0</string></value>
<value><int>1792450695</int></value>
<value><string>add py3 file flit_core-3.12.0-py3-none-any.whl</string></value>
<value><int>42463945</int></value>
</data></array></value>
<value><array><data>
<value><string>cloudxpl</string></value>
<value><nil/></value>
<value><int>1792450705</int></value>
<value><string>create</string></value>
<value><int>42463956</int></value>
</data></array></value>
</data></array></value>
</param></params></methodResponse>`
	client := &httpxtest.MockClient{
		Calls:        []httpxtest.Call{{Method: "POST", URL: "https://pypi.org/pypi", Response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}}},
		URLValidator: httpxtest.NewURLValidator(t),
	}
	got, err := Changelog{Client: client}.Since(context.Background(), 42463944)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	want := []Change{
		{Project: "Flit.Core", Version: "3.12.0", Time: time.Unix(1792450695, 0).UTC(), Action: "add py3 file flit_core-3.12.0-py3-none-any.whl", Serial: 42463945},
		{Project: "cloudxpl", Time: time.Unix(1792450705, 0).UTC(), Action: "create", Serial: 42463956},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Since mismatch (-want +got):\n%s", diff)
	}
	if f, ok := got[0].UploadedFile(); !ok || f != "flit_core-3.12.0-py3-none-any.whl" {
		t.Errorf("UploadedFile() = %q, %v", f, ok)
	}
	if _, ok := got[1].UploadedFile(); ok {
		t.Error("a create event reported an uploaded file")
	}
}

func TestChangelogFault(t *testing.T) {
	body := `<?xml version='1.0'?><methodResponse><fault><value><struct>
<member><name>faultCode</name><value><int>-32500</int></value></member>
<member><name>faultString</name><value><string>RuntimeError: too many requests</string></value></member>
</struct></value></fault></methodResponse>`
	client := &httpxtest.MockClient{
		Calls:             []httpxtest.Call{{Response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}}},
		SkipURLValidation: true,
	}
	_, err := Changelog{Client: client}.LastSerial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too many requests") {
		t.Errorf("LastSerial error = %v, want the fault string", err)
	}
}
