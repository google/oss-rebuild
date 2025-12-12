// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

// "\r\nMetadata-Version: 2.1\r\nName: pandas-read-xml\r\nVersion: 0.3.0\r\nSummary: A tool to read XML files as pandas dataframes.\r\nHome-page: https://github.com/minchulkim87/pandas_read_xml\r\nAuthor: Min Chul Kim\r\nAuthor-email: minchulkim87@gmail.com\r\nLicense: UNKNOWN\r\nPlatform: UNKNOWN\r\nClassifier: Programming Language :: Python :: 3\r\nClassifier: License :: OSI Approved :: MIT License\r\nClassifier: Operating System :: OS Independent\r\nRequires-Python: >=3.6\r\nDescription-Content-Type: text/markdown\r\nRequires-Dist: pyarrow\r\nRequires-Dist: pandas\r\nRequires-Dist: xmltodict\r\nRequires-Dist: requests\r\nRequires-Dist: zipfile36\r\nRequires-Dist: distlib\r\nRequires-Dist: urllib3 (>=1.26.3)\r\n\r\n# Pandas Read XML\r\n\r\nA tool to help read XML files as pandas dataframes.\r\n\r\nSee example in [Google Colab here](https://colab.research.google.com/github/minchulkim87/pandas_read_xml/blob/master/pandas_read_xml_example.ipynb)\r\n\r\nIsn't it annoying working with data in XML format? I think so. Take a look at this simple example.\r\n\r\n```xml\r\n<first-tag>    \r\n<not-interested>        \r\nblah blah    \r\n</not-interested>    \r\n<second-tag>        \r\n<the-tag-you-want-as-root>            \r\n<row>                \r\n<columnA>                    \r\nThe data that you want                \r\n</columnA>                \r\n<columnB>                    \r\nMore data that you want                \r\n</columnB>            \r\n</row>            \r\n<row>                \r\n<columnA>                    \r\nYet more data that you want                \r\n</columnA>                \r\n<columnB>                    \r\nEh, get this data too                \r\n</columnB>            \r\n</row>        \r\n</the-tag-you-want-as-root>    \r\n</second-tag>    \r\n<another-irrelevant-tag>        \r\nsome other info that you do not want    \r\n</another-irrelevant-tag>\r\n</first-tag>\r\n```\r\n\r\nI wish there was a simple `df = pd.read_xml('some_file.xml')` like `pd.read_csv()` and `pd.read_json()` that we all love.\r\n\r\nI can't solve this with my time and skills, but perhaps this package will help get you started.\r\n\r\n\r\n## Install\r\n\r\n```bash\r\npip install pandas_read_xml\r\n```\r\n\r\n## Import package\r\n\r\n```python\r\nimport pandas_read_xml as pdx\r\n```\r\n\r\n## Read XML as pandas dataframe\r\n\r\nYou will need to identify the path to the \"root\" tag in the XML from which you want to extract the data.\r\n\r\n```python\r\ndf = pdx.read_xml(\"test.xml\", ['first-tag', 'second-tag', 'the-tag-you-want-as-root'])\r\n```\r\n\r\nBy default, pandas-read-xml will treat the root tag as being the \"rows\" of the pandas dataframe. If this is not true, pass the argument `root_is_rows=False`.\r\n\r\n*Sometimes, the XML structure is such that pandas will treat rows vs columns in a way that we think are opposites. For these cases, the read_xml may fail. Try using `transpose=True` as an argument in such cases. This argument will only affect the reading if `root_is_rows=False` is passed.\r\n\r\n# Auto Flatten\r\n\r\nThe real cumbersome part of working with XML data (or JSON data) is that they do not represent a single table. Rather, they are a (nested) tree representations of what probably were relational databases. Often, these XML data are exported without a clearly documented schema, and more often, no clear way of navigating the data.\r\n\r\nWhat is even more annoying is that, in comparison to JSON, the data structures are not consistent across XML files from the same schema. Some files may have multiples of the same tag, resulting in a list-type data, while in other files of the *same* schema will only have on of that tag, resulting in a non-list-type data. In other times, the tags are not present which means that the resulting \"column\" is not just null, but not even a column. This makes it difficult to \"flatten\".\r\n\r\nPandas already has some tools to help \"explode\" (items in list become separate rows) and \"normalise\" (key, value pairs in one column become separate columns of data), but they fail when there are these mixed types within the same tags (columns). Besides, \"flattening\" (combining exploding and normalising) duplicates other data in the dataframe as well, leading to an explosion of memory requirements.\r\n\r\nSo, in this tool, I have also attempted to make a few different tools to separate the relational tables.\r\n\r\nSee the example in Colab (or run the notebook elsewhere)\r\n\r\nThe `auto_separate_tables` method will separate out what it guesses to be separate tables. The resulting `data` is a dictionary where the keys are the \"table names\" and the corresponding values are the pandas dataframes. Each of the separate tables will have the `key_columns` as common columns.\r\n\r\nYou can see the list of separated tables by using python dictionary methods.\r\n\r\n```python\r\ndata.keys()\r\n```\r\n\r\nAnd then view the table of interest.\r\n\r\nThere are also other \"smaller\" functions that does parts of the job:\r\n\r\n- flatten(df)\r\n- auto_flatten(df, key_columns)\r\n- fully_flatten(df, key_columns)\r\n\r\nEven more if you look through the code."

func TestStabilizePypi(t *testing.T) {
	testCases := []struct {
		test            string
		input           []*archive.ZipEntry
		expected        []*archive.ZipEntry
		pypiStabilizers []Stabilizer
	}{
		{
			test: "metadata.json stabilization (removal)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "metadata.json"}, Body: []byte("{\"Test\": \"Example\"}")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "metadata.json"}, Body: []byte("This needed to change (metadata)")},
			},
			pypiStabilizers: []Stabilizer{RemoveMetadataJSON},
		},
		{
			test: "dist-info/METADATA stabilization (re-ordering)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "dist-info/METADATA"}, Body: []byte("Metadata-Version: 2.1\r\nName: testProject\r\nVersion: 0.3.0\r\nSummary: A simple test project\r\nHome-page: https://github.com/fake/testProject\r\nAuthor: Fake Author\r\nAuthor-email: fake@gmail.com\r\nLicense: UNKNOWN\r\nPlatform: UNKNOWN\r\nClassifier: Programming Language :: Python :: 3\r\nClassifier: License :: OSI Approved :: MIT License\r\nClassifier: Operating System :: OS Independent\r\nRequires-Python: >=3.6\r\nDescription-Content-Type: text/markdown\r\nRequires-Dist: pyarrow\r\nRequires-Dist: pandas\r\nRequires-Dist: xmltodict\r\nRequires-Dist: requests\r\nRequires-Dist: zipfile36\r\nRequires-Dist: distlib\r\nRequires-Dist: urllib3 (>=1.26.3)\r\n\r\n# Pandas Read XML\r\n\r\nA tool to help read XML files as pandas dataframes.\r\n\r\nSee example in [Google Colab here](https://colab.research.google.com/github/minchulkim87/pandas_read_xml/blob/master/pandas_read_xml_example.ipynb)\r\n\r\nIsn't it annoying working with data in XML format? I think so. Take a look at this simple example.\r\n\r\n```xml\r\n<first-tag>\r\n    <not-interested>\r\n        blah blah\r\n    </not-interested>\r\n    <second-tag>\r\n        <the-tag-you-want-as-root>\r\n            <row>\r\n                <columnA>\r\n                    The data that you want\r\n                </columnA>\r\n                <columnB>\r\n                    More data that you want\r\n                </columnB>\r\n            </row>\r\n            <row>\r\n                <columnA>\r\n                    Yet more data that you want\r\n                </columnA>\r\n                <columnB>\r\n                    Eh, get this data too\r\n                </columnB>\r\n            </row>\r\n        </the-tag-you-want-as-root>\r\n    </second-tag>\r\n    <another-irrelevant-tag>\r\n        some other info that you do not want\r\n    </another-irrelevant-tag>\r\n</first-tag>\r\n```\r\n\r\nI wish there was a simple `df = pd.read_xml('some_file.xml')` like `pd.read_csv()` and `pd.read_json()` that we all love.\r\n\r\nI can't solve this with my time and skills, but perhaps this package will help get you started.\r\n\r\n\r\n## Install\r\n\r\n```bash\r\npip install pandas_read_xml\r\n```\r\n\r\n## Import package\r\n\r\n```python\r\nimport pandas_read_xml as pdx\r\n```\r\n\r\n## Read XML as pandas dataframe\r\n\r\nYou will need to identify the path to the \"root\" tag in the XML from which you want to extract the data.\r\n\r\n```python\r\ndf = pdx.read_xml(\"test.xml\", ['first-tag', 'second-tag', 'the-tag-you-want-as-root'])\r\n```\r\n\r\nBy default, pandas-read-xml will treat the root tag as being the \"rows\" of the pandas dataframe. If this is not true, pass the argument `root_is_rows=False`.\r\n\r\n*Sometimes, the XML structure is such that pandas will treat rows vs columns in a way that we think are opposites. For these cases, the read_xml may fail. Try using `transpose=True` as an argument in such cases. This argument will only affect the reading if `root_is_rows=False` is passed.\r\n\r\n# Auto Flatten\r\n\r\nThe real cumbersome part of working with XML data (or JSON data) is that they do not represent a single table. Rather, they are a (nested) tree representations of what probably were relational databases. Often, these XML data are exported without a clearly documented schema, and more often, no clear way of navigating the data.\r\n\r\nWhat is even more annoying is that, in comparison to JSON, the data structures are not consistent across XML files from the same schema. Some files may have multiples of the same tag, resulting in a list-type data, while in other files of the *same* schema will only have on of that tag, resulting in a non-list-type data. In other times, the tags are not present which means that the resulting \"column\" is not just null, but not even a column. This makes it difficult to \"flatten\".\r\n\r\nPandas already has some tools to help \"explode\" (items in list become separate rows) and \"normalise\" (key, value pairs in one column become separate columns of data), but they fail when there are these mixed types within the same tags (columns). Besides, \"flattening\" (combining exploding and normalising) duplicates other data in the dataframe as well, leading to an explosion of memory requirements.\r\n\r\nSo, in this tool, I have also attempted to make a few different tools to separate the relational tables.\r\n\r\nSee the example in Colab (or run the notebook elsewhere)\r\n\r\nThe `auto_separate_tables` method will separate out what it guesses to be separate tables. The resulting `data` is a dictionary where the keys are the \"table names\" and the corresponding values are the pandas dataframes. Each of the separate tables will have the `key_columns` as common columns.\r\n\r\nYou can see the list of separated tables by using python dictionary methods.\r\n\r\n```python\r\ndata.keys()\r\n```\r\n\r\nAnd then view the table of interest.\r\n\r\nThere are also other \"smaller\" functions that does parts of the job:\r\n\r\n- flatten(df)\r\n- auto_flatten(df, key_columns)\r\n- fully_flatten(df, key_columns)\r\n\r\nEven more if you look through the code.\r\n\r\n\r\n")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "dist-info/METADATA"}, Body: []byte("Author: Fake Author\nAuthor-Email: fake@gmail.com\nClassifier: License :: OSI Approved :: MIT License\nClassifier: Operating System :: OS Independent\nClassifier: Programming Language :: Python :: 3\nDescription-Content-Type: text/markdown\nHome-Page: https://github.com/fake/testProject\nLicense: UNKNOWN\nMetadata-Version: 2.1\nName: testProject\nPlatform: UNKNOWN\nRequires-Dist: distlib\nRequires-Dist: pandas\nRequires-Dist: pyarrow\nRequires-Dist: requests\nRequires-Dist: urllib3 (>=1.26.3)\nRequires-Dist: xmltodict\nRequires-Dist: zipfile36\nRequires-Python: >=3.6\nSummary: A simple test project\nVersion: 0.3.0\n\n")},
			},
			pypiStabilizers: []Stabilizer{StableWheelBuildMetadata},
		},
		// Need another here to test the other kind of description header for stablebuildwheel above
		{
			test: "DESCRIPTION.rst stabilization (removal)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "DESCRIPTION.rst"}, Body: []byte("Test Example")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "DESCRIPTION.rst"}, Body: []byte("This needed to change (description)")},
			},
			pypiStabilizers: []Stabilizer{StablePypiDescription},
		},
		{
			test: "**.py Comment stabilization (augmentation)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test.py"}, Body: []byte("# This is a test file\nprint('Hello, World!')\ndef test(inp):\n	\"\"\"\n	Args: inp - test input\n	\"\"\"\n	return inp\n")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test.py"}, Body: []byte("\nprint('Hello, World!')\ndef test(inp):\n\t\n\n	return inp\n")},
			},
			pypiStabilizers: []Stabilizer{StableCommentsCollapse},
		},
		{
			test: "**_version.py stabilization (removal)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo_version.py"}, Body: []byte("print(\"Test Example\")")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo_version.py"}, Body: []byte("This needed to change (version file)")},
			},
			pypiStabilizers: []Stabilizer{StableVersionFile2},
		},
		{
			test: "version.py TYPE_CHECKING stabilization (pattern replacement)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "package/version.py"}, Body: []byte("# file generated by setuptools_scm\n# don't change, don't track in version control\nTYPE_CHECKING = False\nif TYPE_CHECKING:\n    from typing import Tuple, Union\n    VERSION_TUPLE = Tuple[Union[int, str], ...]\nelse:\n    VERSION_TUPLE = object\n\nversion: str\n__version__: str\n__version_tuple__: VERSION_TUPLE\nversion_tuple: VERSION_TUPLE\n\n__version__ = version = '2.23.4'\n__version_tuple__ = version_tuple = (2, 23, 4)\n")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "package/version.py"}, Body: []byte("# file generated by setuptools_scm\n# don't change, don't track in version control\n\n__all__ = [\"__version__\", \"__version_tuple__\", \"version\", \"version_tuple\"]\n\nTYPE_CHECKING = False\nif TYPE_CHECKING:\n    from typing import Tuple\n    from typing import Union\n\n    VERSION_TUPLE = Tuple[Union[int, str], ...]\nelse:\n    VERSION_TUPLE = object\n\nversion: str\n__version__: str\n__version_tuple__: VERSION_TUPLE\nversion_tuple: VERSION_TUPLE\n\n__version__ = version = '2.23.4'\n__version_tuple__ = version_tuple = (2, 23, 4)\n")},
			},
			pypiStabilizers: []Stabilizer{StableVersionFile},
		},
		{
			test: "crlf stabilization (replacement)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "main.py"}, Body: []byte("print(\"Test Example\")\r\nprint(\"Again\")\r\n")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "main.py"}, Body: []byte("print(\"Test Example\")\nprint(\"Again\")\n")},
			},
			pypiStabilizers: []Stabilizer{StableCrlf},
		},
		{
			test: "RECORD stabilization (recalculation)",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "main.py"}, Body: []byte("print(\"Test Example\")\r\n")},
				{FileHeader: &zip.FileHeader{Name: "RECORD"}, Body: []byte("main.py,sha256=m4Zko9OiLRzzuYT2lK_OqeuQYu8c3SBEQ0OGXfigXaA,23\n")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "main.py"}, Body: []byte("print(\"Test Example\")\n")},
				{FileHeader: &zip.FileHeader{Name: "RECORD"}, Body: []byte("main.py,sha256=JS6wpAIal6Ln6jkjf27iP4mJQFWyNJ4bHzxpxc8a_Gc,22\n")},
			},
			pypiStabilizers: []Stabilizer{StableCrlf, StablePypiRecord},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			// Construct zip from tc.input
			var input bytes.Buffer
			{
				zw := zip.NewWriter(&input)
				for _, entry := range tc.input {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
			}
			var output bytes.Buffer
			zr := must(zip.NewReader(bytes.NewReader(input.Bytes()), int64(input.Len())))

			err := StabilizeZip(zr, zip.NewWriter(&output), StabilizeOpts{Stabilizers: tc.pypiStabilizers})
			if err != nil {
				t.Fatalf("StabilizeZip(%v) = %v, want nil", tc.test, err)
			}
			var got []archive.ZipEntry
			{
				zr := must(zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())))
				for _, ent := range zr.File {
					got = append(got, archive.ZipEntry{FileHeader: &ent.FileHeader, Body: must(io.ReadAll(must(ent.Open())))})
				}
			}
			if len(got) != len(tc.expected) {
				t.Fatalf("StabilizeZip(%v) = %v, want %v", tc.test, got, tc.expected)
			}
			for i := range got {
				test1 := got[i].FileHeader.Name == tc.expected[i].FileHeader.Name
				test2 := bytes.Equal(got[i].Body, tc.expected[i].Body)
				test3 := got[i].FileHeader.Modified.Equal(tc.expected[i].FileHeader.Modified)
				test4 := got[i].FileHeader.Comment == tc.expected[i].FileHeader.Comment
				test5 := string(got[i].Body)
				test6 := string(tc.expected[i].Body)
				if !test2 {
					fmt.Printf("%v, %v, %v, %v", test1, test2, test3, test4)
					fmt.Printf("\nCaptured File:\n[%v]\n", test5)
					fmt.Printf("\nExpected File:\n[%v]\n", test6)
				}
				if !all(
					got[i].FileHeader.Name == tc.expected[i].FileHeader.Name,
					bytes.Equal(got[i].Body, tc.expected[i].Body),
				) {
					t.Fatalf("StabilizeZip(%v) = %v, want %v", tc.test, got, tc.expected)
				}
			}
		})
	}
}
