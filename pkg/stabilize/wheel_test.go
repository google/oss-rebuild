// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
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

func recordDigestLine(path string, body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%s,sha256=%s,%d\n", path, base64.RawURLEncoding.EncodeToString(sum[:]), len(body))
}

func TestStableWheelCycloneDXSBOM(t *testing.T) {
	if StableWheelCycloneDXSBOM.Ordering >= StableWheelRecord.Ordering {
		t.Fatalf("StableWheelCycloneDXSBOM.Ordering (%d) must run before StableWheelRecord.Ordering (%d)", StableWheelCycloneDXSBOM.Ordering, StableWheelRecord.Ordering)
	}
	const (
		recordPath    = "uuid_utils-0.17.1.dist-info/RECORD"
		wantCargoSBOM = `{"bomFormat":"CycloneDX","components":[{"bom-ref":"registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3","name":"pyo3","purl":"pkg:cargo/pyo3@0.28.3","type":"library","version":"0.28.3"}],"dependencies":[{"dependsOn":["registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3"],"ref":"path+file:///#uuid-utils@0.17.1"}],"metadata":{"component":{"bom-ref":"path+file:///#uuid-utils@0.17.1","components":[{"bom-ref":"path+file:///#uuid-utils@0.17.1 bin-target-0","name":"uuid_utils","purl":"pkg:cargo/uuid-utils@0.17.1?download_url=file://.#src/lib.rs","type":"library","version":"0.17.1"}],"name":"uuid-utils","purl":"pkg:cargo/uuid-utils@0.17.1?download_url=file:///","type":"library","version":"0.17.1"},"tools":[{"name":"cargo-cyclonedx","vendor":"CycloneDX","version":"0.5.9"}]},"specVersion":"1.4","version":1}`
	)
	for _, tc := range []struct {
		name       string
		entryPath  string
		entryBody  string
		wantBody   string
		wantRecord string
	}{
		{
			name:      "NormalizesCargoCycloneDXAndUpdatesRecord",
			entryPath: "uuid_utils-0.17.1.dist-info/sboms/uuid-utils.cyclonedx.json",
			entryBody: "{\r\n" +
				`  "bomFormat": "CycloneDX",` + "\r\n" +
				`  "specVersion": "1.4",` + "\r\n" +
				`  "version": 1,` + "\r\n" +
				`  "serialNumber": "urn:uuid:2e730067-6ac9-4824-aef2-ccd29f63bcfc",` + "\r\n" +
				`  "metadata": {` + "\r\n" +
				`    "timestamp": "2026-05-19T07:40:45.208581000Z",` + "\r\n" +
				`    "tools": [{"vendor": "CycloneDX", "name": "cargo-cyclonedx", "version": "0.5.9"}],` + "\r\n" +
				`    "component": {` + "\r\n" +
				`      "type": "library",` + "\r\n" +
				`      "bom-ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1",` + "\r\n" +
				`      "name": "uuid-utils",` + "\r\n" +
				`      "version": "0.17.1",` + "\r\n" +
				`      "purl": "pkg:cargo/uuid-utils@0.17.1?download_url=file:///home/runner/work/uuid-utils/uuid-utils",` + "\r\n" +
				`      "components": [{` + "\r\n" +
				`        "type": "library",` + "\r\n" +
				`        "bom-ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1 bin-target-0",` + "\r\n" +
				`        "name": "uuid_utils",` + "\r\n" +
				`        "version": "0.17.1",` + "\r\n" +
				`        "purl": "pkg:cargo/uuid-utils@0.17.1?download_url=file://.#src/lib.rs"` + "\r\n" +
				`      }]` + "\r\n" +
				`    }` + "\r\n" +
				`  },` + "\r\n" +
				`  "components": [{` + "\r\n" +
				`    "type": "library",` + "\r\n" +
				`    "bom-ref": "registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3",` + "\r\n" +
				`    "name": "pyo3",` + "\r\n" +
				`    "version": "0.28.3",` + "\r\n" +
				`    "purl": "pkg:cargo/pyo3@0.28.3"` + "\r\n" +
				`  }],` + "\r\n" +
				`  "dependencies": [{` + "\r\n" +
				`    "ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1",` + "\r\n" +
				`    "dependsOn": ["registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3"]` + "\r\n" +
				`  }]` + "\r\n" +
				"}\r\n",
			wantBody: wantCargoSBOM,
			wantRecord: recordDigestLine("uuid_utils-0.17.1.dist-info/sboms/uuid-utils.cyclonedx.json", []byte(wantCargoSBOM)) +
				recordPath + ",,\n",
		},
		{
			name:      "PreservesNonCycloneDXJSONBytes",
			entryPath: "pkg-1.0.dist-info/sboms/spdx.json",
			entryBody: "{\n  \"bomFormat\": \"SPDX\",\n  \"serialNumber\": \"urn:uuid:keep-me\"\n}\n",
			wantBody:  "{\n  \"bomFormat\": \"SPDX\",\n  \"serialNumber\": \"urn:uuid:keep-me\"\n}\n",
		},
		{
			name:      "PreservesMalformedJSONBytes",
			entryPath: "pkg-1.0.dist-info/sboms/broken.cyclonedx.json",
			entryBody: `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234",`,
			wantBody:  `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234",`,
		},
		{
			name:      "PreservesTrailingGarbageJSONBytes",
			entryPath: "pkg-1.0.dist-info/sboms/trailing.cyclonedx.json",
			entryBody: `{"bomFormat": "CycloneDX"} trailing`,
			wantBody:  `{"bomFormat": "CycloneDX"} trailing`,
		},
		{
			name:      "PreservesNonObjectJSONBytes",
			entryPath: "pkg-1.0.dist-info/sboms/null.cyclonedx.json",
			entryBody: "  null\n",
			wantBody:  "  null\n",
		},
		{
			name:      "PreservesDuplicateKeyBytes",
			entryPath: "pkg-1.0.dist-info/sboms/dup.cyclonedx.json",
			entryBody: `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234", "components": [{"name": "a"}], "components": [{"name": "b"}]}`,
			wantBody:  `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234", "components": [{"name": "a"}], "components": [{"name": "b"}]}`,
		},
		{
			name:      "PreservesInvalidUTF8Bytes",
			entryPath: "pkg-1.0.dist-info/sboms/bad-utf8.cyclonedx.json",
			entryBody: "{\"bomFormat\": \"CycloneDX\", \"serialNumber\": \"urn:uuid:1234\", \"name\": \"\xff\"}",
			wantBody:  "{\"bomFormat\": \"CycloneDX\", \"serialNumber\": \"urn:uuid:1234\", \"name\": \"\xff\"}",
		},
		{
			name:      "PreservesLoneSurrogateEscapeBytes",
			entryPath: "pkg-1.0.dist-info/sboms/surrogate.cyclonedx.json",
			entryBody: `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234", "name": "\ud800"}`,
			wantBody:  `{"bomFormat": "CycloneDX", "serialNumber": "urn:uuid:1234", "name": "\ud800"}`,
		},
		{
			name:      "IgnoresCycloneDXOutsideDistInfoSBOMs",
			entryPath: "pkg/data/sbom.cyclonedx.json",
			entryBody: "{\n  \"bomFormat\": \"CycloneDX\",\n  \"serialNumber\": \"urn:uuid:keep-me\"\n}\n",
			wantBody:  "{\n  \"bomFormat\": \"CycloneDX\",\n  \"serialNumber\": \"urn:uuid:keep-me\"\n}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := must(archivetest.ZipFile([]archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: tc.entryPath}, Body: []byte(tc.entryBody)},
				{FileHeader: &zip.FileHeader{Name: recordPath}, Body: []byte("stale,sha256=bad,1\n")},
			})).Bytes()
			var out bytes.Buffer
			orDie(Stabilize(&out, bytes.NewReader(in), archive.ZipFormat))
			zrOut := must(zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len())))
			bodies := make(map[string]string, len(zrOut.File))
			for _, f := range zrOut.File {
				bodies[f.Name] = string(must(io.ReadAll(must(f.Open()))))
			}
			if diff := cmp.Diff(tc.wantBody, bodies[tc.entryPath]); diff != "" {
				t.Errorf("SBOM body diff (-want +got):\n%s", diff)
			}
			if tc.wantRecord != "" {
				if diff := cmp.Diff(tc.wantRecord, bodies[recordPath]); diff != "" {
					t.Errorf("RECORD body diff (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestStableWheelCycloneDXSBOM_EquivalenceAndMismatch(t *testing.T) {
	makeWheel := func(t *testing.T, cargoSBOM, extraSBOM string) []byte {
		t.Helper()
		entries := []archive.ZipEntry{
			{FileHeader: &zip.FileHeader{Name: "uuid_utils/__init__.py"}, Body: []byte("__version__ = '0.17.1'\n")},
			{FileHeader: &zip.FileHeader{Name: "uuid_utils-0.17.1.dist-info/sboms/uuid-utils.cyclonedx.json"}, Body: []byte(cargoSBOM)},
			{FileHeader: &zip.FileHeader{Name: "uuid_utils-0.17.1.dist-info/sboms/sbom.json"}, Body: []byte(extraSBOM)},
			{
				FileHeader: &zip.FileHeader{Name: "uuid_utils-0.17.1.dist-info/RECORD"},
				Body: []byte(
					recordDigestLine("uuid_utils/__init__.py", []byte("__version__ = '0.17.1'\n")) +
						recordDigestLine("uuid_utils-0.17.1.dist-info/sboms/uuid-utils.cyclonedx.json", []byte(cargoSBOM)) +
						recordDigestLine("uuid_utils-0.17.1.dist-info/sboms/sbom.json", []byte(extraSBOM)) +
						"uuid_utils-0.17.1.dist-info/RECORD,,\n",
				),
			},
		}
		var out bytes.Buffer
		orDie(Stabilize(&out, bytes.NewReader(must(archivetest.ZipFile(entries)).Bytes()), archive.ZipFormat))
		return out.Bytes()
	}
	upstreamCargo := `{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "version": 1,
  "serialNumber": "urn:uuid:aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb",
  "metadata": {
    "timestamp": "2026-09-30T12:11:20.111183600Z",
    "component": {
      "type": "library",
      "bom-ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1",
      "name": "uuid-utils",
      "version": "0.17.1",
      "purl": "pkg:cargo/uuid-utils@0.17.1?download_url=file:///home/runner/work/uuid-utils/uuid-utils",
      "components": [{
        "type": "library",
        "bom-ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1 bin-target-0",
        "name": "uuid_utils",
        "version": "0.17.1"
      }]
    }
  },
  "components": [{
    "type": "library",
    "bom-ref": "registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.1",
    "name": "uuid",
    "version": "1.23.1",
    "purl": "pkg:cargo/uuid@1.23.1"
  }],
  "dependencies": [{
    "ref": "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1",
    "dependsOn": ["registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.1"]
  }]
}`
	rebuiltCargo := `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"serialNumber":"urn:uuid:cccccccc-4444-5555-6666-dddddddddddd","metadata":{"timestamp":"2026-11-02T15:00:00Z","component":{"type":"library","bom-ref":"path+file:///src#uuid-utils@0.17.1","name":"uuid-utils","version":"0.17.1","purl":"pkg:cargo/uuid-utils@0.17.1?download_url=file:///src","components":[{"type":"library","bom-ref":"path+file:///src#uuid-utils@0.17.1 bin-target-0","name":"uuid_utils","version":"0.17.1"}]}},"components":[{"type":"library","bom-ref":"registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.1","name":"uuid","version":"1.23.1","purl":"pkg:cargo/uuid@1.23.1"}],"dependencies":[{"ref":"path+file:///src#uuid-utils@0.17.1","dependsOn":["registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.1"]}]}`
	upstreamExtra := "{\r\n  \"bomFormat\": \"CycloneDX\",\r\n  \"specVersion\": \"1.5\",\r\n  \"version\": 1,\r\n  \"serialNumber\": \"urn:uuid:fd5505e0-bfbb-4b63-a519-d916bdf43baf\",\r\n  \"metadata\": {\"timestamp\": \"2026-09-29T23:08:25Z\"},\r\n  \"components\": [{\"type\": \"library\", \"name\": \"openssl\", \"version\": \"4.0.3\"}]\r\n}\r\n"
	rebuiltExtra := `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"serialNumber":"urn:uuid:99999999-0000-0000-0000-111111111111","metadata":{"timestamp":"2026-11-02T15:05:00Z"},"components":[{"type":"library","name":"openssl","version":"4.0.3"}]}`
	differentCrateVerCargo := `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"serialNumber":"urn:uuid:cccccccc-4444-5555-6666-dddddddddddd","metadata":{"timestamp":"2026-11-02T15:00:00Z","component":{"type":"library","bom-ref":"path+file:///src#uuid-utils@0.17.1","name":"uuid-utils","version":"0.17.1","purl":"pkg:cargo/uuid-utils@0.17.1?download_url=file:///src","components":[{"type":"library","bom-ref":"path+file:///src#uuid-utils@0.17.1 bin-target-0","name":"uuid_utils","version":"0.17.1"}]}},"components":[{"type":"library","bom-ref":"registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.2","name":"uuid","version":"1.23.2","purl":"pkg:cargo/uuid@1.23.2"}],"dependencies":[{"ref":"path+file:///src#uuid-utils@0.17.1","dependsOn":["registry+https://github.com/rust-lang/crates.io-index#uuid@1.23.2"]}]}`
	differentOpenSSLExtra := `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"serialNumber":"urn:uuid:99999999-0000-0000-0000-111111111111","metadata":{"timestamp":"2026-11-02T15:05:00Z"},"components":[{"type":"library","name":"openssl","version":"4.0.4"}]}`
	for _, tc := range []struct {
		name      string
		cargoB    string
		extraB    string
		wantEqual bool
	}{
		{
			name:      "HostCheckoutTimestampAndSerialDifferencesMatch",
			cargoB:    rebuiltCargo,
			extraB:    rebuiltExtra,
			wantEqual: true,
		},
		{
			name:      "CargoDependencyVersionDifferenceMismatches",
			cargoB:    differentCrateVerCargo,
			extraB:    rebuiltExtra,
			wantEqual: false,
		},
		{
			name:      "BundledComponentVersionDifferenceMismatches",
			cargoB:    rebuiltCargo,
			extraB:    differentOpenSSLExtra,
			wantEqual: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotA := makeWheel(t, upstreamCargo, upstreamExtra)
			gotB := makeWheel(t, tc.cargoB, tc.extraB)
			if gotEqual := bytes.Equal(gotA, gotB); gotEqual != tc.wantEqual {
				t.Errorf("bytes.Equal(stabilizedA, stabilizedB) = %v, want %v", gotEqual, tc.wantEqual)
			}
		})
	}
}

func TestStripCargoPathSpecHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "LinuxCheckoutOmittedCrateName",
			in:   "path+file:///home/runner/work/uuid-utils/uuid-utils#0.17.1",
			want: "path+file:///#uuid-utils@0.17.1",
		},
		{
			name: "ContainerRootExplicitCrateName",
			in:   "path+file:///src#uuid-utils@0.17.1",
			want: "path+file:///#uuid-utils@0.17.1",
		},
		{
			name: "WindowsCheckoutBinTarget",
			in:   "path+file:///C:/a/pydantic/pydantic/pydantic-core#2.50.1 bin-target-0",
			want: "path+file:///#pydantic-core@2.50.1 bin-target-0",
		},
		{
			name: "WorkspaceSubcratesStayDistinct",
			in:   "path+file:///home/runner/work/cryptography/cryptography/src/rust/cryptography-cffi#0.50.2",
			want: "path+file:///#cryptography-cffi@0.50.2",
		},
		{
			name: "CratesIORegistryRefUnchanged",
			in:   "registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3",
			want: "registry+https://github.com/rust-lang/crates.io-index#pyo3@0.28.3",
		},
		{
			name: "PyPIPurlRefUnchanged",
			in:   "pkg:pypi/klayout@0.30.7?file_name=klayout.whl",
			want: "pkg:pypi/klayout@0.30.7?file_name=klayout.whl",
		},
		{
			name: "PathFileWithoutFragmentUnchanged",
			in:   "path+file:///home/runner/work/uuid-utils",
			want: "path+file:///home/runner/work/uuid-utils",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, stripCargoPathSpecHost(tc.in)); diff != "" {
				t.Errorf("stripCargoPathSpecHost(%q) diff (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}
