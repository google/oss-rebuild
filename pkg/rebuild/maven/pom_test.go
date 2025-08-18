// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
)

type TestRegistry struct {
	releaseFile string
}

func (TestRegistry) PackageMetadata(_ context.Context, _ string) (*mavenreg.MavenPackage, error) {
	return nil, nil
}

func (TestRegistry) PackageVersion(_ context.Context, _, _ string) (*mavenreg.MavenVersion, error) {
	return nil, nil
}

func (r TestRegistry) ReleaseFile(_ context.Context, _, _, _ string) (io.ReadCloser, error) {
	reader := strings.NewReader(r.releaseFile)
	return io.NopCloser(reader), nil
}

func (r TestRegistry) ReleaseURL(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}

func TestNewPomXML(t *testing.T) {
	tests := []struct {
		testName string
		input    string
		expected PomXML
		isError  bool
	}{
		{
			testName: "parse_artifact_id",
			input:    "<project><artifactId>ARTIFACT_ID</artifactId></project>",
			expected: PomXML{
				ArtifactID: "ARTIFACT_ID",
			},
		},
		{
			testName: "parse_artifact_id_with_attributes",
			input:    "<project><artifactId what=\"not\">ARTIFACT_ID</artifactId></project>",
			expected: PomXML{
				ArtifactID: "ARTIFACT_ID",
			},
		},
		{
			testName: "parse_parent",
			input:    "<project><parent><groupId>PARENT_GROUP_ID</groupId></parent><groupId>GROUP_ID</groupId></project>",
			expected: PomXML{
				GroupID: "GROUP_ID",
				Parent: &PomXML{
					GroupID: "PARENT_GROUP_ID",
				},
			},
		},
		{
			testName: "empty_parse_error",
			input:    "",
			expected: PomXML{},
			isError:  true,
		},
		{
			testName: "random_parse_error",
			input:    "<something>",
			expected: PomXML{},
			isError:  true,
		},
		{
			testName: "no_fields_parse",
			input:    "<project/>",
			expected: PomXML{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.testName, func(t *testing.T) {
			ctx := context.Background()
			target := rebuild.Target{}
			mux := rebuild.RegistryMux{
				Maven: TestRegistry{
					releaseFile: tc.input,
				},
			}

			pom, err := NewPomXML(ctx, target, mux)
			if err != nil {
				if !tc.isError {
					t.Fatalf("NewPomXML() error = %v", err)
				}
				return
			} else if tc.isError {
				t.Fatalf("NewPomXML() expected error but no error returned")
			}
			if diff := cmp.Diff(tc.expected, pom); diff != "" {
				t.Errorf("NewPomXML() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
