package netclassify

import (
	"errors"
	"testing"
)

func TestClassifyURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr error
	}{
		// Docker URL tests
		{
			name: "valid docker manifest URL",
			url:  "https://registry-1.docker.io/v2/library/alpine/manifests/3.19",
			want: "pkg:docker/library/alpine@3.19",
		},
		{
			name: "docker manifest with hash",
			url:  "https://registry-1.docker.io/v2/library/alpine/manifests/sha256:ae65dbf8749a7d4527648ccee1fa3deb6bfcae34cbc30fc67aa45c44dcaa90ee",
			want: "pkg:docker/library/alpine@sha256:ae65dbf8749a7d4527648ccee1fa3deb6bfcae34cbc30fc67aa45c44dcaa90ee",
		},
		{
			name: "docker blob",
			url:  "https://registry-1.docker.io/v2/library/alpine/blobs/sha256:ae65dbf8749a7d4527648ccee1fa3deb6bfcae34cbc30fc67aa45c44dcaa90ee",
			want: "pkg:docker-blob/library/alpine@sha256:ae65dbf8749a7d4527648ccee1fa3deb6bfcae34cbc30fc67aa45c44dcaa90ee",
		},
		{
			name:    "skipped docker blob upload",
			url:     "https://registry-1.docker.io/v2/library/alpine/blobs/uploads",
			wantErr: ErrSkipped,
		},

		// git URL tests
		{
			name: "valid git URL",
			url:  "https://github.com/foo/bar/git-upload-pack",
			want: "pkg:github/foo/bar",
		},
		{
			name:    "skipped git URL",
			url:     "https://github.com/foo/bar/git-receive-pack",
			wantErr: ErrSkipped,
		},

		// Alpine URL tests
		{
			name: "valid alpine package URL",
			url:  "https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64/git-2.43.5-r0.apk",
			want: "pkg:alpine/git@2.43.5-r0",
		},
		{
			name: "alpine community package URL",
			url:  "https://dl-cdn.alpinelinux.org/alpine/v3.19/community/x86_64/some_package-1.0.0-r0.apk",
			want: "pkg:alpine/some_package@1.0.0-r0",
		},
		{
			name:    "invalid alpine URL format",
			url:     "https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64/invalid_format.apk",
			want:    "",
			wantErr: ErrUnclassified,
		},

		// PyPI URL tests
		{
			name: "valid PyPI wheel URL",
			url:  "https://files.pythonhosted.org/packages/84/c2/80633736cd183ee4a62107413def345f7e6e3c01563dbca1417363cf957e/build-1.2.2.post1-py3-none-any.whl",
			want: "pkg:pypi/build@1.2.2.post1",
		},
		{
			name:    "PyPI metadata URL",
			url:     "https://files.pythonhosted.org/packages/84/c2/80633736cd183ee4a62107413def345f7e6e3c01563dbca1417363cf957e/build-1.2.2.post1-py3-none-any.whl.metadata",
			want:    "",
			wantErr: ErrSkipped,
		},
		{
			name:    "PyPI API URL",
			url:     "https://pypi.org/simple/build/",
			want:    "",
			wantErr: ErrSkipped,
		},
		{
			name: "PyPI source distribution URL",
			url:  "https://files.pythonhosted.org/packages/84/c2/80633736cd183ee4a62107413def345f7e6e3c01563dbca1417363cf957e/sample-1.0.0.tar.gz",
			want: "pkg:pypi/sample@1.0.0",
		},
		{
			name:    "invalid PyPI URL",
			url:     "https://files.pythonhosted.org/packages/invalid/format",
			want:    "",
			wantErr: ErrUnclassified,
		},

		// gcs URL tests
		{
			name: "valid GCS URL",
			url:  "https://foo.storage.googleapis.com/bar/baz",
			want: "pkg:generic/gcs/foo/bar/baz",
		},
		{
			name: "valid xml GCS URL",
			url:  "https://storage.googleapis.com/download/storage/v1/b/foo/o/bar/baz",
			want: "pkg:generic/gcs/foo/bar/baz",
		},

		// Other cases
		{
			name:    "unknown URL",
			url:     "https://example.com/invalid",
			want:    "",
			wantErr: ErrUnclassified,
		},
		{
			name:    "empty URL",
			url:     "",
			want:    "",
			wantErr: ErrUnclassified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClassifyURL(tt.url)
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("ClassifyURL() error = nil, wantErr %v", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("ClassifyURL() error = %v, wantErr %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("ClassifyURL() unexpected error = %v", err)
			} else if got != tt.want {
				t.Errorf("ClassifyURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyPyPIFile(t *testing.T) {
	tests := []struct {
		name    string
		fname   string
		want    string
		wantErr error
	}{
		{
			name:  "valid wheel file",
			fname: "package-1.0.0-py3-none-any.whl",
			want:  "pkg:pypi/package@1.0.0",
		},
		{
			name:    "metadata file",
			fname:   "package-1.0.0-py3-none-any.whl.metadata",
			wantErr: ErrSkipped,
		},
		{
			name:    "egg file",
			fname:   "package-1.0.0.egg",
			wantErr: ErrUnclassified,
		},
		{
			name:  "source distribution tar.gz",
			fname: "package-1.0.0.tar.gz",
			want:  "pkg:pypi/package@1.0.0",
		},
		{
			name:  "source distribution zip",
			fname: "package-1.0.0.zip",
			want:  "pkg:pypi/package@1.0.0",
		},
		{
			name:    "invalid filename format",
			fname:   "invalid-format",
			wantErr: ErrBadPySource,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyPyPIFile(tt.fname)
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("classifyPyPIFile() error = nil, wantErr %v", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("classifyPyPIFile() error = %v, wantErr %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("classifyPyPIFile() unexpected error = %v", err)
			} else if got != tt.want {
				t.Errorf("classifyPyPIFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
