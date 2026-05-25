// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TestStableWheelRecord(t *testing.T) {
	var (
		aPy      = []byte("a body")           // sha256=dxgk3kIodkL6D74LVouAzDKQ4pt9PNbvvHsbjrEDewQ,6
		bPy      = []byte("b body")           // sha256=_X2n133q0-hx2JPbRU34B8coLBIdH_ZW47KETAnJy_A,6
		commaPy  = []byte("comma body")       // sha256=DcHOMGvJkogcR6NcEfJFjhF_vIfwoLCTZkYCurlCyso,10
		initPy   = []byte(nil)                // sha256=47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU,0
		metadata = []byte("metadata content") // sha256=gvqVnixuRLh5zPiHSC2d2-Dk89UQwBUeOusKBd1BqIA,16
		wheel    = []byte("wheel content")    // sha256=qX_WAEgebn3-t67SQSDj6augPFNkxq13_UDzu98eQq0,13
	)
	for _, tc := range []struct {
		name           string
		input          []*archive.ZipEntry
		wantRecordPath string // empty = expect no RECORD in output
		wantRecord     string
	}{
		{
			name: "non-wheel zip is a no-op",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo/bar.txt"}, Body: []byte("hi")},
			},
		},
		{
			name: "recomputes digests and sorts by path",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/METADATA"}, Body: metadata},
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/WHEEL"}, Body: wheel},
				{FileHeader: &zip.FileHeader{Name: "pkg/b.py"}, Body: bPy},
				{FileHeader: &zip.FileHeader{Name: "pkg/a.py"}, Body: aPy},
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/RECORD"}, Body: []byte("stale,sha256=XXX,99\n")},
			},
			wantRecordPath: "pkg-1.0.dist-info/RECORD",
			wantRecord: "pkg-1.0.dist-info/METADATA,sha256=gvqVnixuRLh5zPiHSC2d2-Dk89UQwBUeOusKBd1BqIA,16\n" +
				"pkg-1.0.dist-info/WHEEL,sha256=qX_WAEgebn3-t67SQSDj6augPFNkxq13_UDzu98eQq0,13\n" +
				"pkg/a.py,sha256=dxgk3kIodkL6D74LVouAzDKQ4pt9PNbvvHsbjrEDewQ,6\n" +
				"pkg/b.py,sha256=_X2n133q0-hx2JPbRU34B8coLBIdH_ZW47KETAnJy_A,6\n" +
				"pkg-1.0.dist-info/RECORD,,\n",
		},
		{
			name: "adds self-entry when missing from input",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "old/a.py"}, Body: aPy},
				{FileHeader: &zip.FileHeader{Name: "old-0.1.dist-info/RECORD"}, Body: []byte("old/a.py,sha256=X,1\n")},
			},
			wantRecordPath: "old-0.1.dist-info/RECORD",
			wantRecord: "old/a.py,sha256=dxgk3kIodkL6D74LVouAzDKQ4pt9PNbvvHsbjrEDewQ,6\n" +
				"old-0.1.dist-info/RECORD,,\n",
		},
		{
			name: "replaces CRLF line endings from input RECORD",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "pkg/a.py"}, Body: aPy},
				{FileHeader: &zip.FileHeader{Name: "pkg/b.py"}, Body: bPy},
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/RECORD"}, Body: []byte("seed\r\n")},
			},
			wantRecordPath: "pkg-1.0.dist-info/RECORD",
			wantRecord: "pkg/a.py,sha256=dxgk3kIodkL6D74LVouAzDKQ4pt9PNbvvHsbjrEDewQ,6\n" +
				"pkg/b.py,sha256=_X2n133q0-hx2JPbRU34B8coLBIdH_ZW47KETAnJy_A,6\n" +
				"pkg-1.0.dist-info/RECORD,,\n",
		},
		{
			name: "lists each duplicate path entry",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "pkg/dup.py"}, Body: []byte("dup1")}, // content differs
				{FileHeader: &zip.FileHeader{Name: "pkg/dup.py"}, Body: []byte("dup2")}, // content differs
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/RECORD"}, Body: nil},
			},
			wantRecordPath: "pkg-1.0.dist-info/RECORD",
			wantRecord: "pkg/dup.py,sha256=SW7Oa5LRG3q3WvgT4GF-ogfuhBBk_nXCZnz5nK8VJww,4\n" +
				"pkg/dup.py,sha256=ZN6mzlh-sH8o2wyHH2fa6XeKQidBjWc3LDLpyUA7F6M,4\n" +
				"pkg-1.0.dist-info/RECORD,,\n",
		},
		{
			name: "records empty files with zero size",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "pkg/__init__.py"}, Body: initPy},
				{FileHeader: &zip.FileHeader{Name: "pkg/a.py"}, Body: aPy},
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/RECORD"}, Body: nil},
			},
			wantRecordPath: "pkg-1.0.dist-info/RECORD",
			wantRecord: "pkg/__init__.py,sha256=47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU,0\n" +
				"pkg/a.py,sha256=dxgk3kIodkL6D74LVouAzDKQ4pt9PNbvvHsbjrEDewQ,6\n" +
				"pkg-1.0.dist-info/RECORD,,\n",
		},
		{
			name: "quotes paths containing commas",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "pkg/z.py"}, Body: bPy},
				{FileHeader: &zip.FileHeader{Name: "pkg/a,b.py"}, Body: commaPy},
				{FileHeader: &zip.FileHeader{Name: "pkg-1.0.dist-info/RECORD"}, Body: nil},
			},
			wantRecordPath: "pkg-1.0.dist-info/RECORD",
			wantRecord: "\"pkg/a,b.py\",sha256=DcHOMGvJkogcR6NcEfJFjhF_vIfwoLCTZkYCurlCyso,10\n" +
				"pkg/z.py,sha256=_X2n133q0-hx2JPbRU34B8coLBIdH_ZW47KETAnJy_A,6\n" +
				"pkg-1.0.dist-info/RECORD,,\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in bytes.Buffer
			zw := zip.NewWriter(&in)
			for _, e := range tc.input {
				orDie(e.WriteTo(zw))
			}
			orDie(zw.Close())

			var out bytes.Buffer
			zr := must(zip.NewReader(bytes.NewReader(in.Bytes()), int64(in.Len())))
			orDie(StabilizeZip(zr, zip.NewWriter(&out), NewContext(archive.ZipFormat).WithStabilizers([]Stabilizer{StableWheelRecord})))

			zrOut := must(zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len())))
			bodies := make(map[string][]byte, len(zrOut.File))
			for _, f := range zrOut.File {
				bodies[f.Name] = must(io.ReadAll(must(f.Open())))
			}
			if tc.wantRecordPath == "" {
				return
			}
			got, ok := bodies[tc.wantRecordPath]
			if !ok {
				t.Fatalf("output is missing %q; got %v", tc.wantRecordPath, keysOf(bodies))
			}
			if string(got) != tc.wantRecord {
				t.Errorf("RECORD body mismatch:\ngot:  %q\nwant: %q", got, tc.wantRecord)
			}
		})
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestStableWheelRecord_Ordering(t *testing.T) {
	if StableWheelRecord.Ordering != StageFinalize {
		t.Errorf("StableWheelRecord.Ordering = %d, want StageFinalize (%d)", StableWheelRecord.Ordering, StageFinalize)
	}
}
