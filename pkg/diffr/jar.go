// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// classFileMagic is the magic number for Java class files (0xCAFEBABE)
var classFileMagic = []byte{0xCA, 0xFE, 0xBA, 0xBE}

// classFileReader wraps a byte slice with position tracking for parsing
type classFileReader struct {
	data []byte
	pos  int
}

func newClassFileReader(data []byte) *classFileReader {
	return &classFileReader{data: data, pos: 0}
}

func (r *classFileReader) readU2() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, errors.New("read beyond end of class file")
	}
	val := binary.BigEndian.Uint16(r.data[r.pos : r.pos+2])
	r.pos += 2
	return val, nil
}

func (r *classFileReader) readU4() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, errors.New("read beyond end of class file")
	}
	val := binary.BigEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return val, nil
}

func (r *classFileReader) skip(n int) error {
	if r.pos+n > len(r.data) {
		return errors.New("skip beyond end of class file")
	}
	r.pos += n
	return nil
}

func (r *classFileReader) readBytes(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, errors.New("read beyond end of class file")
	}
	data := r.data[r.pos : r.pos+n]
	r.pos += n
	return data, nil
}

func (r *classFileReader) readU1() (uint8, error) {
	if r.pos+1 > len(r.data) {
		return 0, errors.New("read beyond end of class file")
	}
	val := r.data[r.pos]
	r.pos += 1
	return val, nil
}

// disassembleClassFile parses a Java class file and returns UTF-8 strings and method opcodes
func disassembleClassFile(data []byte) (string, error) {
	if len(data) < 8 {
		return "", errors.New("class file too short")
	}
	// Check magic number
	if !bytes.Equal(data[0:4], classFileMagic) {
		return "", errors.New("invalid class file magic number")
	}
	// Initialize output builder
	var output strings.Builder
	r := newClassFileReader(data)
	if err := r.skip(4); err != nil {
		return "", errors.Wrap(err, "skipping magic number")
	}
	// Read minor and major version
	minor, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading minor version")
	}
	major, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading major version")
	}
	output.WriteString(fmt.Sprintf("Class file version: %d.%d\n", major, minor))

	// Read constant pool count
	cpCount, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading constant pool count")
	}
	// Parse constant pool - only keep UTF-8 strings
	var utf8Strings []string
	for i := uint16(1); i < cpCount; i++ {
		tag, err := r.readU1()
		if err != nil {
			return "", errors.Wrapf(err, "reading constant pool tag at index %d", i)
		}
		switch tag {
		case 1: // UTF8
			length, err := r.readU2()
			if err != nil {
				return "", errors.Wrapf(err, "reading UTF8 length at index %d", i)
			}
			bytes, err := r.readBytes(int(length))
			if err != nil {
				return "", errors.Wrapf(err, "reading UTF8 bytes at index %d", i)
			}
			utf8Strings = append(utf8Strings, string(bytes))
		case 5, 6: // Long, Double - take two slots
			if err := r.skip(8); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
			i++ // Increment again for the second slot
		case 3: // Integer
			if err := r.skip(4); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
		case 4: // Float
			if err := r.skip(4); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
		case 7, 8: // Class, String
			if err := r.skip(2); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
		case 9, 10, 11: // Fieldref, Methodref, InterfaceMethodref
			if err := r.skip(4); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
		case 12: // NameAndType
			if err := r.skip(4); err != nil {
				return "", errors.Wrapf(err, "skipping constant pool entry at index %d", i)
			}
		default:
			return "", fmt.Errorf("unknown constant pool tag: %d at index %d", tag, i)
		}
	}
	// Output UTF-8 strings
	if len(utf8Strings) > 0 {
		output.WriteString("UTF-8 strings:\n")
		for _, str := range utf8Strings {
			output.WriteString(fmt.Sprintf("  %s\n", str))
		}
	}
	// Skip access flags, this class, super class
	_, err = r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading access flags")
	}
	_, err = r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading this class")
	}
	_, err = r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading super class")
	}
	// Skip interfaces
	interfacesCount, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading interfaces count")
	}
	for i := uint16(0); i < interfacesCount; i++ {
		_, err = r.readU2()
		if err != nil {
			return "", errors.Wrapf(err, "reading interface %d", i)
		}
	}
	// Skip fields
	fieldsCount, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading fields count")
	}
	for i := uint16(0); i < fieldsCount; i++ {
		_, err = r.readU2() // access flags
		if err != nil {
			return "", errors.Wrapf(err, "reading field %d flags", i)
		}
		_, err = r.readU2() // name index
		if err != nil {
			return "", errors.Wrapf(err, "reading field %d name", i)
		}
		_, err = r.readU2() // descriptor index
		if err != nil {
			return "", errors.Wrapf(err, "reading field %d descriptor", i)
		}
		attributesCount, err := r.readU2()
		if err != nil {
			return "", errors.Wrapf(err, "reading field %d attributes count", i)
		}
		// Skip all field attributes
		for j := uint16(0); j < attributesCount; j++ {
			_, err := r.readU2() // attribute name index
			if err != nil {
				return "", errors.Wrapf(err, "reading field %d attribute %d name", i, j)
			}
			attrLength, err := r.readU4()
			if err != nil {
				return "", errors.Wrapf(err, "reading field %d attribute %d length", i, j)
			}
			_, err = r.readBytes(int(attrLength))
			if err != nil {
				return "", errors.Wrapf(err, "reading field %d attribute %d data", i, j)
			}
		}
	}
	// Read methods and extract opcodes from Code attributes
	methodsCount, err := r.readU2()
	if err != nil {
		return "", errors.Wrap(err, "reading methods count")
	}
	if methodsCount > 0 {
		output.WriteString("Method opcodes:\n")
		for i := uint16(0); i < methodsCount; i++ {
			_, err = r.readU2() // access flags
			if err != nil {
				return "", errors.Wrapf(err, "reading method %d flags", i)
			}
			_, err = r.readU2() // name index
			if err != nil {
				return "", errors.Wrapf(err, "reading method %d name", i)
			}
			_, err = r.readU2() // descriptor index
			if err != nil {
				return "", errors.Wrapf(err, "reading method %d descriptor", i)
			}
			attributesCount, err := r.readU2()
			if err != nil {
				return "", errors.Wrapf(err, "reading method %d attributes count", i)
			}
			// Look for Code attribute and extract opcodes
			for j := uint16(0); j < attributesCount; j++ {
				_, err = r.readU2() // attribute name index
				if err != nil {
					return "", errors.Wrapf(err, "reading method %d attribute %d name", i, j)
				}
				attrLength, err := r.readU4()
				if err != nil {
					return "", errors.Wrapf(err, "reading method %d attribute %d length", i, j)
				}
				// Try to parse as Code attribute (structure: max_stack(2) + max_locals(2) + code_length(4) + code + ...)
				codeAttrStart := r.pos
				_, err = r.readU2() // max_stack
				if err != nil {
					// Not a Code attribute, skip it
					r.pos = codeAttrStart
					_, err = r.readBytes(int(attrLength))
					if err != nil {
						return "", errors.Wrapf(err, "skipping method %d attribute %d", i, j)
					}
					continue
				}
				_, err = r.readU2() // max_locals
				if err != nil {
					r.pos = codeAttrStart
					_, err = r.readBytes(int(attrLength))
					if err != nil {
						return "", errors.Wrapf(err, "skipping method %d attribute %d", i, j)
					}
					continue
				}
				codeLength, err := r.readU4() // code_length
				if err != nil {
					r.pos = codeAttrStart
					_, err = r.readBytes(int(attrLength))
					if err != nil {
						return "", errors.Wrapf(err, "skipping method %d attribute %d", i, j)
					}
					continue
				}
				// Verify code_length is reasonable (must fit within attribute)
				if codeLength > attrLength-8 {
					r.pos = codeAttrStart
					_, err = r.readBytes(int(attrLength))
					if err != nil {
						return "", errors.Wrapf(err, "skipping method %d attribute %d", i, j)
					}
					continue
				}
				// Read the bytecode
				code, err := r.readBytes(int(codeLength))
				if err != nil {
					r.pos = codeAttrStart
					_, err = r.readBytes(int(attrLength))
					if err != nil {
						return "", errors.Wrapf(err, "skipping method %d attribute %d", i, j)
					}
					continue
				}
				// Successfully parsed as Code attribute - format opcodes as hex
				output.WriteString(fmt.Sprintf("  Method %d:\n", i))
				for k, opcode := range code {
					if k > 0 && k%16 == 0 {
						output.WriteString("\n")
					}
					output.WriteString(fmt.Sprintf(" %02x", opcode))
				}
				output.WriteString("\n")
				// Skip remaining Code attribute data (exception table and attributes)
				remaining := int(attrLength) - (2 + 2 + 4 + int(codeLength))
				if remaining > 0 {
					_, err = r.readBytes(remaining)
					if err != nil {
						return "", errors.Wrapf(err, "reading method %d Code attribute remainder", i)
					}
				}
			}
		}
	}

	return output.String(), nil
}

// isClassFile checks if a file name ends with .class
func isClassFile(name string) bool {
	return strings.HasSuffix(name, ".class")
}

// compareClassFiles compares two class files by disassembling them and diffing the output
// If the files are not valid class files, it falls back to binary comparison
func compareClassFiles(node *DiffNode, file1, file2 File) (bool, error) {
	// Read both files
	content1, err := readAll(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "reading file1")
	}
	content2, err := readAll(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "reading file2")
	}
	// Check if identical
	if bytes.Equal(content1, content2) {
		return true, nil
	}
	// Try to disassemble both files
	disassembly1, err1 := disassembleClassFile(content1)
	disassembly2, err2 := disassembleClassFile(content2)
	// If either file is not a valid class file, fall back to binary comparison
	if err1 != nil || err2 != nil {
		node.Comments = []string{"Binary files differ (not valid class files)"}
		return false, nil
	}
	// Compare disassembled output using text diff
	if disassembly1 == disassembly2 {
		return true, nil
	}
	// Generate unified diff of disassembled output
	diff, err := gitdiff.Strings(disassembly1, disassembly2)
	if err != nil {
		return false, errors.Wrap(err, "generating diff of disassembled class files")
	}
	if diff != "" {
		node.UnifiedDiff = &diff
	}
	return false, nil
}

// compareJar compares two JAR archives
// JAR files are ZIP files, so we reuse the ZIP comparison logic
// but add special handling for .class files
func compareJar(ctx compareContext, node *DiffNode, file1, file2 File) (bool, error) {
	// Get file sizes
	size1, err := getSize(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "getting size of file1")
	}
	size2, err := getSize(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "getting size of file2")
	}
	// Open both zip files
	zr1, err := zip.NewReader(file1.Reader.(io.ReaderAt), size1)
	if err != nil {
		return false, errors.Wrap(err, "opening jar file1")
	}
	zr2, err := zip.NewReader(file2.Reader.(io.ReaderAt), size2)
	if err != nil {
		return false, errors.Wrap(err, "opening jar file2")
	}
	// Create maps for entries
	entries1 := make(map[string]*zip.File)
	entries2 := make(map[string]*zip.File)
	// Generate file listings
	var listing1, listing2 strings.Builder
	for _, f := range zr1.File {
		entries1[f.Name] = f
		listing1.WriteString(formatZipListing(&f.FileHeader))
	}
	for _, f := range zr2.File {
		entries2[f.Name] = f
		listing2.WriteString(formatZipListing(&f.FileHeader))
	}
	// Compare listings
	match := true
	if listingStr1, listingStr2 := listing1.String(), listing2.String(); listingStr1 != listingStr2 {
		match = false
		listingDiff, err := gitdiff.Strings(listingStr1, listingStr2)
		if err != nil {
			return false, errors.Wrap(err, "diffing jar listings")
		}
		if listingDiff != "" {
			listingNode := DiffNode{
				Source1:     "file list",
				Source2:     "file list",
				UnifiedDiff: &listingDiff,
			}
			node.Details = append(node.Details, listingNode)
		}
	}
	// Get all unique entry names
	allNames := make(map[string]bool)
	for name := range entries1 {
		allNames[name] = true
	}
	for name := range entries2 {
		allNames[name] = true
	}
	// Sort names for consistent ordering
	var sortedNames []string
	for name := range allNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	// Compare individual entries
	for _, name := range sortedNames {
		e1, has1 := entries1[name]
		e2, has2 := entries2[name]
		if !has1 && has2 {
			// Entry only in file2
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  name,
				Source2:  name,
				Comments: []string{commentOnlyInSecond},
			})
		} else if has1 && !has2 {
			// Entry only in file1
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  name,
				Source2:  name,
				Comments: []string{commentOnlyInFirst},
			})
		} else if has1 && has2 {
			// Entry in both - compare contents
			entryNode := DiffNode{
				Source1: name,
				Source2: name,
			}
			// Open and compare entry contents
			r1, err := e1.Open()
			if err != nil {
				return false, errors.Wrapf(err, "opening %s in file1", name)
			}
			defer r1.Close()
			r2, err := e2.Open()
			if err != nil {
				return false, errors.Wrapf(err, "opening %s in file2", name)
			}
			defer r2.Close()
			// Buffer for comparison
			buf1 := new(bytes.Buffer)
			buf2 := new(bytes.Buffer)
			io.Copy(buf1, r1)
			io.Copy(buf2, r2)
			entryFile1 := File{
				Name:   name,
				Reader: bytes.NewReader(buf1.Bytes()),
			}
			entryFile2 := File{
				Name:   name,
				Reader: bytes.NewReader(buf2.Bytes()),
			}
			// Special handling for class files
			var entryMatch bool
			if isClassFile(name) {
				entryMatch, err = compareClassFiles(&entryNode, entryFile1, entryFile2)
				if err != nil {
					return false, errors.Wrapf(err, "comparing class file %s", name)
				}
			} else {
				// For non-class files, use the standard comparison
				entryMatch, err = compareFiles(ctx.Child(), &entryNode, entryFile1, entryFile2)
				if err != nil {
					return false, errors.Wrapf(err, "comparing %s", name)
				}
			}
			if !entryMatch {
				match = false
				node.Details = append(node.Details, entryNode)
			}
		}
	}
	return match, nil
}
