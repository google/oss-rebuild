package npm

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	reg "github.com/google/oss-rebuild/pkg/registry/npm"
)

func TestPickNodeVersion(t *testing.T) {
	tests := []struct {
		name        string
		nodeVersion string
		want        string
		wantErr     bool
	}{
		{
			name:        "empty version returns default",
			nodeVersion: "",
			want:        "10.17.0",
		},
		{
			name:        "exact version match",
			nodeVersion: "16.13.0",
			want:        "16.13.0",
		},
		{
			name:        "trust the future",
			nodeVersion: "24.6.1",
			want:        "24.6.1",
		},
		{
			name:        "node 8 upgrades to default",
			nodeVersion: "8.15.0",
			want:        "8.16.2",
		},
		{
			name:        "node 9 upgrades to 10",
			nodeVersion: "9.0.0",
			want:        "10.16.3",
		},
		{
			name:        "invalid semver returns error",
			nodeVersion: "not.a.version",
			want:        "",
			wantErr:     true,
		},
		{
			name:        "very old version falls back to appropriate default",
			nodeVersion: "6.0.0",
			want:        "8.16.2",
		},
		{
			name:        "handles non-MUSL versions correctly",
			nodeVersion: "13.10.0", // Exists but no MUSL
			want:        "13.10.1", // Exists and has MUSL
		},
		{
			name:        "non-existent defaults to highest patch version of next highest release",
			nodeVersion: "14.14.1",
			want:        "14.15.5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PickNodeVersion(&reg.NPMVersion{NodeVersion: tt.nodeVersion})
			if (err != nil) != tt.wantErr {
				t.Errorf("PickNodeVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("PickNodeVersion() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
