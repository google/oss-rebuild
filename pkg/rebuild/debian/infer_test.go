// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	debreg "github.com/google/oss-rebuild/pkg/registry/debian"
)

func TestInferStrategy(t *testing.T) {
	target := rebuild.Target{
		Ecosystem: rebuild.Debian,
		Package:   "main/xz-utils",
		Version:   "5.2.4-1",
		Artifact:  "xz-utils_5.2.4-1_amd64.deb",
	}
	// NOTE: A response body can only be read once so each case needs its own call.
	buildInfoCall := func() httpxtest.Call {
		return httpxtest.Call{
			URL: "https://buildinfos.debian.net/buildinfo-pool/x/xz-utils/xz-utils_5.2.4-1_amd64.buildinfo",
			Response: &http.Response{
				StatusCode: http.StatusOK,
				Body: httpxtest.Body(textwrap.Dedent(`
					Format: 1.0
					Source: xz-utils
					Binary: xz-utils
					Architecture: amd64
					Version: 5.2.4-1
					Build-Architecture: amd64
					Build-Path: /build/xz-utils-abc/xz-utils-5.2.4
					Installed-Build-Depends:
					 dpkg (= 1.22.13)
					Environment:
					 DEB_BUILD_OPTIONS="parallel=4"`)[1:]),
			},
		}
	}
	debootsnapSbuild := &DebootsnapSbuild{
		BuildInfo: FileWithChecksum{
			URL: "https://buildinfos.debian.net/buildinfo-pool/x/xz-utils/xz-utils_5.2.4-1_amd64.buildinfo",
		},
		Architecture:      "amd64",
		BuildArch:         "amd64",
		HostArch:          "amd64",
		SrcPackage:        "xz-utils",
		SrcVersion:        "5.2.4-1",
		SrcVersionNoEpoch: "5.2.4-1",
		BuildPath:         "/build/xz-utils-abc",
		DscDir:            "xz-utils-abc",
		DpkgVersion:       "1.22.13",
		Env:               []string{"DEB_BUILD_OPTIONS=parallel=4"},
	}
	tests := []struct {
		name    string
		hint    rebuild.Strategy
		calls   []httpxtest.Call
		want    rebuild.Strategy
		wantErr bool
	}{
		{
			name:  "DefaultDebootsnapSbuild",
			calls: []httpxtest.Call{buildInfoCall()},
			want:  debootsnapSbuild,
		},
		{
			name:  "LocationHint",
			hint:  &rebuild.LocationHint{Location: rebuild.Location{Repo: "https://salsa.debian.org/debian/xz-utils"}},
			calls: []httpxtest.Call{buildInfoCall()},
			want:  debootsnapSbuild,
		},
		{
			name: "DebianPackageHint",
			hint: &DebianPackage{},
			calls: []httpxtest.Call{
				{
					URL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1.dsc",
					Response: &http.Response{
						StatusCode: http.StatusOK,
						Body: httpxtest.Body(textwrap.Dedent(`
							Format: 3.0 (quilt)
							Source: xz-utils
							Build-Depends: debhelper (>= 8.9.0), autopoint
							Files:
							 003e4d0b1b1899fc6e3000b24feddf7c 1053868 xz-utils_5.2.4.orig.tar.xz
							 5d018428dac6a83f00c010f49c51836e 135296 xz-utils_5.2.4-1.debian.tar.xz`)[1:]),
					},
				},
			},
			want: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1.dsc",
				},
				Orig: FileWithChecksum{
					URL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4.orig.tar.xz",
					MD5: "003e4d0b1b1899fc6e3000b24feddf7c",
				},
				Debian: FileWithChecksum{
					URL: "https://deb.debian.org/debian/pool/main/x/xz-utils/xz-utils_5.2.4-1.debian.tar.xz",
					MD5: "5d018428dac6a83f00c010f49c51836e",
				},
				Requirements: []string{"debhelper", "autopoint"},
			},
		},
		{
			name: "DebrebuildHint",
			hint: &Debrebuild{},
			want: &Debrebuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/x/xz-utils/xz-utils_5.2.4-1_amd64.buildinfo",
				},
			},
		},
		{
			name: "DebrebuildNoCheckHint",
			hint: &Debrebuild{UseNoCheck: true},
			want: &Debrebuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/x/xz-utils/xz-utils_5.2.4-1_amd64.buildinfo",
				},
				UseNoCheck: true,
			},
		},
		{
			name:    "UnsupportedHint",
			hint:    &rebuild.ManualStrategy{},
			calls:   []httpxtest.Call{buildInfoCall()},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &httpxtest.MockClient{
				Calls:        tc.calls,
				URLValidator: httpxtest.NewURLValidator(t),
			}
			mux := rebuild.RegistryMux{
				Debian: debreg.HTTPRegistry{Client: client},
			}
			got, err := Rebuilder{}.InferStrategy(context.Background(), target, mux, nil, tc.hint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("InferStrategy() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("InferStrategy() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("InferStrategy() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
