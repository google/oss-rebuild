// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestReleaseURL(t *testing.T) {
	testCases := []struct {
		test     string
		artifact string
		version  string
		filetype string
		want     string
	}{
		{
			test:     "guava_pom",
			artifact: "com.google.guava:guava",
			version:  "33.4.8-jre",
			filetype: TypePOM,
			want:     "https://repo1.maven.org/maven2/com/google/guava/guava/33.4.8-jre/guava-33.4.8-jre.pom",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			r := HTTPRegistry{}
			url, err := r.ReleaseURL(context.Background(), tc.artifact, tc.version, tc.filetype)
			if err != nil {
				t.Fatalf("ReleaseURL() error = %v", err)
			}
			if url != tc.want {
				t.Errorf("ReleaseURL() = %v, want %v", url, tc.want)
			}
		})
	}
}

func TestReleaseTime(t *testing.T) {
	const pom = "https://repo1.maven.org/maven2/junit/junit/4.13.2/junit-4.13.2.pom"
	for _, tc := range []struct {
		name    string
		resp    *http.Response
		want    time.Time
		wantErr bool
	}{
		{
			name: "LastModified",
			resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Last-Modified": {"Sat, 13 Feb 2021 16:31:36 GMT"}}, Body: httpxtest.Body("")},
			want: time.Date(2021, time.February, 13, 16, 31, 36, 0, time.UTC),
		},
		{
			name:    "MissingRelease",
			resp:    &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: httpxtest.Body("")},
			wantErr: true,
		},
		{
			name:    "NoLastModified",
			resp:    &http.Response{StatusCode: http.StatusOK, Body: httpxtest.Body("")},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := HTTPRegistry{Client: &httpxtest.MockClient{
				Calls:        []httpxtest.Call{{Method: http.MethodHead, URL: pom, Response: tc.resp}},
				URLValidator: httpxtest.NewURLValidator(t),
			}}
			got, err := r.ReleaseTime(context.Background(), "junit:junit", "4.13.2")
			if (err != nil) != tc.wantErr {
				t.Fatalf("ReleaseTime() error = %v, want error %t", err, tc.wantErr)
			}
			if !got.Equal(tc.want) {
				t.Errorf("ReleaseTime() = %v, want %v", got, tc.want)
			}
		})
	}
}
