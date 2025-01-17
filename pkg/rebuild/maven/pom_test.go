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
				Parent: Parent{
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

			if pom, err := NewPomXML(ctx, target, mux); err != nil && !tc.isError {
				t.Fatalf("NewPomXML() error = %v", err)
			} else if pom.ArtifactID != tc.expected.ArtifactID {
				t.Errorf("NewPomXML() mismatch pom.ArtifactID expected=%s got=%s", tc.expected.ArtifactID, pom.ArtifactID)
			} else if pom.GroupID != tc.expected.GroupID {
				t.Errorf("NewPomXML() mismatch pom.GroupID expected=%s got=%s", tc.expected.GroupID, pom.GroupID)
			} else if pom.VersionID != tc.expected.VersionID {
				t.Errorf("NewPomXML() mismatch pom.VersionID expected=%s got=%s", tc.expected.VersionID, pom.VersionID)
			} else if diff := cmp.Diff(tc.expected, pom); diff != "" {
				t.Errorf("NewPomXML() cmp.Diff mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
