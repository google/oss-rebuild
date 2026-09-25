// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	regdeb "github.com/google/oss-rebuild/pkg/registry/debian"
)

func TestInferDebootsnapSbuild(t *testing.T) {
	testCases := []struct {
		name      string
		target    rebuild.Target
		buildinfo string
		want      *DebootsnapSbuild
		wantErr   bool
	}{
		{
			name: "PopulatesBuildPathAndDscDir",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/xz-utils",
				Version:   "0:5.4.1-0.2",
				Artifact:  "xz-utils_5.4.1-0.2_amd64.deb",
			},
			buildinfo: `Format: 1.0
Source: xz-utils
Binary: xz-utils
Architecture: amd64 source
Version: 5.4.1-0.2
Build-Architecture: amd64
Build-Path: /build/reproducible-path/xz-utils-5.4.1
Installed-Build-Depends:
 autoconf (= 2.69-11),
 dpkg (= 1.22.11)
Environment:
 DEB_BUILD_OPTIONS="parallel=4"
 LANG="C.UTF-8"
`,
			want: &DebootsnapSbuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/x/xz-utils/xz-utils_5.4.1-0.2_amd64.buildinfo",
				},
				Architecture:      "amd64 source",
				BuildArch:         "amd64",
				HostArch:          "amd64",
				SrcPackage:        "xz-utils",
				SrcVersion:        "5.4.1-0.2",
				SrcVersionNoEpoch: "5.4.1-0.2",
				BuildPath:         "/build/reproducible-path",
				DscDir:            "xz-utils-5.4.1",
				DpkgVersion:       "1.22.11",
				ForceNoRoot:       true,
				Env:               []string{"DEB_BUILD_OPTIONS=parallel=4", "LANG=C.UTF-8"},
			},
		},
		{
			name: "EpochAndBinNMUWithNewerDpkg",
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "main/ffmpeg",
				Version:   "7:8.1.2-2+b3",
				Artifact:  "ffmpeg_8.1.2-2+b3_amd64.deb",
			},
			buildinfo: `Format: 1.0
Source: ffmpeg (7:8.1.2-2)
Binary: ffmpeg
Architecture: amd64
Version: 7:8.1.2-2+b3
Binary-Only-Changes:
 ffmpeg (7:8.1.2-2+b3) sid; urgency=low, binary-only=yes
Build-Architecture: amd64
Host-Architecture: arm64
Build-Path: /build/ffmpeg-abc/ffmpeg-8.1.2
Installed-Build-Depends:
 dpkg (= 1.22.15)
`,
			want: &DebootsnapSbuild{
				BuildInfo: FileWithChecksum{
					URL: "https://buildinfos.debian.net/buildinfo-pool/f/ffmpeg/ffmpeg_8.1.2-2+b3_amd64.buildinfo",
				},
				Architecture:      "amd64",
				BuildArch:         "amd64",
				HostArch:          "arm64",
				SrcPackage:        "ffmpeg",
				SrcVersion:        "7:8.1.2-2",
				SrcVersionNoEpoch: "8.1.2-2",
				BuildPath:         "/build/ffmpeg-abc",
				DscDir:            "ffmpeg-8.1.2",
				DpkgVersion:       "1.22.15",
				ForceNoRoot:       false,
				BinaryOnlyChanges: "ffmpeg (7:8.1.2-2+b3) sid; urgency=low, binary-only=yes",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						URL: tc.want.BuildInfo.URL,
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(tc.buildinfo),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			mux := rebuild.RegistryMux{
				Debian: regdeb.HTTPRegistry{Client: mockClient},
			}
			r := Rebuilder{}
			got, err := r.InferStrategy(context.Background(), tc.target, mux, nil, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("InferStrategy() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("InferStrategy() mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

func TestInferStrategy_DebrebuildHint(t *testing.T) {
	target := rebuild.Target{
		Ecosystem: rebuild.Debian,
		Package:   "main/acl",
		Version:   "0:2.3.2-2",
		Artifact:  "acl_2.3.2-2_amd64.deb",
	}
	r := Rebuilder{}
	got, err := r.InferStrategy(context.Background(), target, rebuild.RegistryMux{}, nil, &Debrebuild{UseNoCheck: true})
	if err != nil {
		t.Fatalf("InferStrategy() unexpected error: %v", err)
	}
	want := &Debrebuild{
		BuildInfo: FileWithChecksum{
			URL: "https://buildinfos.debian.net/buildinfo-pool/a/acl/acl_2.3.2-2_amd64.buildinfo",
		},
		UseNoCheck: true,
	}
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("InferStrategy() mismatch (-got +want):\n%s", diff)
	}
}
