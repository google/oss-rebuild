package archivetest

import (
	"archive/zip"
	"bytes"

	"github.com/google/oss-rebuild/pkg/archive"
)

func ZipFile(entries []archive.ZipEntry) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for _, entry := range entries {
		fw, err := zw.CreateHeader(entry.FileHeader)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(entry.Body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}
