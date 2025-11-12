package utils

import (
	re "regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

var SupportedFileTypes = []string{"pyproject.toml"}

type FoundFile struct {
	Filename   string
	Filetype   string
	Path       string
	FileObject *object.File
}

type FileVerification struct {
	FoundF       FoundFile
	Type         string
	Path         string
	Main         bool
	NameMatch    bool
	VersionMatch bool
}

var FuzzyThreshold = 90

func SortVerifications(verifications []FileVerification) []FileVerification {
	// Sort verifications by Main, NameMatch, VersionMatch
	sorted := make([]FileVerification, len(verifications))
	copy(sorted, verifications)
	changed := true
	for changed {
		changed = false
		i := 0
		for j := 1; j < len(sorted); j++ {
			change := false
			if sorted[j].VersionMatch && !sorted[i].VersionMatch { // Version requires a name match so it is the best to switch
				change = true
			} else if sorted[i].VersionMatch && !sorted[j].VersionMatch { // Version already better
				// do nothing
			} else if sorted[j].NameMatch && !sorted[i].NameMatch { // Name match is better than no name match
				change = true
			} else if sorted[i].NameMatch && !sorted[j].NameMatch { // Name match already better
				// do nothing
			} else if sorted[j].Main && !sorted[i].Main { // Then finally, if there is no match use the main file
				change = true
			}

			if change {
				sorted[i], sorted[j] = sorted[j], sorted[i]
				changed = true
			}

			i = j
		}
	}
	return sorted
}

func NormalizeName(name string) string {
	// Normalizes a package name according to PEP 503.
	normalized := re.MustCompile(`[-_.]+`).ReplaceAllString(name, "-")
	return strings.ToLower(normalized)
}

// Recursively check for build files
func GoDeep(fileType string, tree *object.Tree, name, version string) ([]FoundFile, error) {

	good := false
	for _, ft := range SupportedFileTypes {
		if ft == fileType {
			good = true
			break
		}
	}
	if !good {
		return nil, errors.New("unsupported file type")
	}

	var foundFiles []FoundFile

	result := tree.Files()
	result.ForEach(func(f *object.File) error {
		if strings.Contains(f.Name, fileType) {
			foundFiles = append(foundFiles, FoundFile{
				Filename: f.Name,
				Filetype: fileType,
				// Path:       strings.Replace(f.Name, fileType, "", 1),
				Path:       strings.Replace(strings.Replace(f.Name, fileType, "", 1), "pkg/parsing/pypi/pyprojectToml/testFiles/", "", 1), // For testing. We can modify the test to work better
				FileObject: f,
			})
		}
		return nil
	})
	if len(foundFiles) == 0 {
		return nil, errors.New("no matching files found")
	}
	return foundFiles, nil
}
