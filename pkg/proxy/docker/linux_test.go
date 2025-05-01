package docker

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestTruststorePath(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string][]byte
		want    string
		wantErr bool
	}{
		{
			name: "debian",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=debian\n"),
			},
			want: "/etc/ssl/certs/ca-certificates.crt",
		},
		{
			name: "ubuntu",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=ubuntu\n"),
			},
			want: "/etc/ssl/certs/ca-certificates.crt",
		},
		{
			name: "gentoo",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=gentoo\n"),
			},
			want: "/etc/ssl/certs/ca-certificates.crt",
		},
		{
			name: "linuxmint",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=linuxmint\n"),
			},
			want: "/etc/ssl/certs/ca-certificates.crt",
		},
		{
			name: "opensuse-leap",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=opensuse-leap\n"),
			},
			want: "/var/lib/ca-certificates/ca-bundle.pem",
		},
		{
			name: "opensuse-tumbleweed",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=opensuse-tumbleweed\n"),
			},
			want: "/var/lib/ca-certificates/ca-bundle.pem",
		},
		{
			name: "kaniko",
			files: map[string][]byte{
				"kaniko/.keep": []byte("some content"),
			},
			want: "/kaniko/ssl/certs/ca-certificates.crt",
		},
		{
			name: "alpine",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=alpine\n"),
			},
			want: "/etc/ssl/cert.pem",
		},
		{
			name: "arch",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=arch\n"),
			},
			want: "/etc/ssl/cert.pem",
		},
		{
			name: "openwrt",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=openwrt\n"),
			},
			want: "/etc/ssl/cert.pem",
		},
		{
			name: "rhel",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=rhel\n"),
			},
			want: "/etc/pki/tls/cert.pem",
		},
		{
			name: "fedora",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=fedora\n"),
			},
			want: "/etc/pki/tls/cert.pem",
		},
		{
			name: "centos",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=centos\n"),
			},
			want: "/etc/pki/tls/cert.pem",
		},
		{
			name: "another distro",
			files: map[string][]byte{
				"etc/os-release": []byte("ID=fake\n"),
			},
			wantErr: true,
		},
		{
			name:    "no files",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			for name, content := range tc.files {
				path := filepath.Join(rootDir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatalf("Failed to create directory: %v", err)
				}
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatalf("Failed to write file: %v", err)
				}
			}
			got, err := TruststorePath(newFakeFS(t, tc.files))
			if tc.wantErr {
				if err == nil {
					t.Errorf("Unexpected success, got nil error wanted error")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: got %v want nil", err)
				}
				if got != tc.want {
					t.Errorf("Locate() returned unexpected result: got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func newFakeFS(t *testing.T, files map[string][]byte) fs.FS {
	tmpDir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	}
	return &fakeFS{rootDir: tmpDir}
}

type fakeFS struct {
	rootDir string
}

func (f *fakeFS) Open(path string) (fs.File, error) {
	return os.Open(filepath.Join(f.rootDir, path))
}
