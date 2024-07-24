package archivetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TarFile(entries []archive.TarEntry) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	for _, entry := range entries {
		entry.Header.Size = int64(len(entry.Body))
		if err := tw.WriteHeader(entry.Header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(entry.Body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func TgzFile(entries []archive.TarEntry) (*bytes.Buffer, error) {
	buf, err := TarFile(entries)
	if err != nil {
		return nil, err
	}
	zbuf := new(bytes.Buffer)
	w := gzip.NewWriter(zbuf)
	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return zbuf, nil
}
