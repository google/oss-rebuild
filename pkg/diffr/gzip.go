// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"

	"github.com/pkg/errors"
)

// compareGzip compares two gzip files
func compareGzip(node *DiffNode, file1, file2 File) (bool, error) {
	// Decompress both files
	gz1, err := gzip.NewReader(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "creating gzip reader for file1")
	}
	defer gz1.Close()
	gz2, err := gzip.NewReader(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "creating gzip reader for file2")
	}
	defer gz2.Close()
	// Buffer the decompressed content for type detection
	buf1 := new(bytes.Buffer)
	buf2 := new(bytes.Buffer)
	if _, err := io.Copy(buf1, gz1); err != nil {
		return false, errors.Wrap(err, "reading gzip content1")
	}
	if _, err := io.Copy(buf2, gz2); err != nil {
		return false, errors.Wrap(err, "reading gzip content2")
	}
	// Create child node for decompressed content
	// Handle .tgz extension naming specially, adding .tar to the base name
	childName1 := file1.Name
	if strings.HasSuffix(childName1, ".tgz") {
		childName1 = strings.TrimSuffix(childName1, ".tgz") + ".tar"
	} else {
		childName1 = strings.TrimSuffix(childName1, ".gz")
	}
	childName2 := file2.Name
	if strings.HasSuffix(childName2, ".tgz") {
		childName2 = strings.TrimSuffix(childName2, ".tgz") + ".tar"
	} else {
		childName2 = strings.TrimSuffix(childName2, ".gz")
	}
	childNode := DiffNode{
		Source1: childName1,
		Source2: childName2,
	}
	// Compare the decompressed content
	childFile1 := File{
		Name:   childNode.Source1,
		Reader: bytes.NewReader(buf1.Bytes()),
	}
	childFile2 := File{
		Name:   childNode.Source2,
		Reader: bytes.NewReader(buf2.Bytes()),
	}
	match, err := compareFiles(&childNode, childFile1, childFile2)
	if err != nil {
		return false, errors.Wrap(err, "comparing decompressed content")
	}
	// Only add details if there are differences
	if !match {
		node.Details = append(node.Details, childNode)
	}
	return match, nil
}
