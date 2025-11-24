package parsing

import (
	"context"
	"log"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

func ExtractAllRequirements(ctx context.Context, tree *object.Tree, name, version string) ([]string, error) {
	log.Println("Extracting any extra requirements from found build file types (pyproject.toml)")
	var reqs []string
	var foundFiles []FoundFile

	foundPyprojFiles, err := GoDeep("pyproject.toml", tree, "", "")
	if err != nil {
		log.Printf("Failed to find pyproject.toml files: %v", err)
	} else {
		foundFiles = append(foundFiles, foundPyprojFiles...)
	}

	// TODO setup.py

	// TODO setup.cfg

	if len(foundFiles) == 0 {
		return nil, errors.New("no supported build files found for requirement extraction")
	}

	var verifiedFiles []FileVerification

	for _, foundFile := range foundFiles {
		switch foundFile.Filetype {
		case "pyproject.toml":
			verification, err := VerifyPyProjectFile(ctx, foundFile, name, version)
			if err != nil {
				log.Printf("Failed to verify pyproject.toml file: %v", err)
				continue
			}
			verifiedFiles = append(verifiedFiles, verification)
		// TODO case setup.py
		// TODO case setup.cfg
		default:
			log.Printf("Unsupported file type for verification: %s", foundFile.Filetype)
		}
	}

	if len(verifiedFiles) == 0 {
		return nil, errors.New("no verified build files found for requirement extraction")
	}

	sortedVerification := SortVerifications(verifiedFiles)

	bestFile := sortedVerification[0]
	dir := bestFile.Path

	posFiles := []FoundFile{bestFile.FoundF}
	for _, f := range foundFiles {
		if f.Path == dir && f.Filename != bestFile.FoundF.Filename {
			posFiles = append(posFiles, f)
		}
	}

	for _, f := range posFiles {
		switch f.Filetype {
		case "pyproject.toml":
			pyprojReqs, err := ExtractPyProjectRequirements(ctx, f.FileObject)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to extract pyproject.toml requirements")
			}

			reqs = append(reqs, pyprojReqs...)
		// TODO case setup.py
		// TODO case setup.cfg
		default:
			log.Printf("Unsupported file type for requirement extraction: %s", f.Filetype)
		}
	}

	return reqs, nil
}
