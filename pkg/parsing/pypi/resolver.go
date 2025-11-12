package pypi

import (
	"context"
	"log"

	"github.com/go-git/go-git/v5/plumbing/object"
	pyprojecttoml "github.com/google/oss-rebuild/pkg/parsing/pypi/pyprojectToml"
	"github.com/google/oss-rebuild/pkg/parsing/pypi/utils"
	"github.com/pkg/errors"
)

func ExtractAllRequirements(ctx context.Context, tree *object.Tree, name, version string) ([]string, error) {
	log.Println("Extracting any extra requirements from found build file types (pyproject.toml)")
	var reqs []string
	var foundFiles []utils.FoundFile
	good := false

	foundPyprojFiles, err := utils.GoDeep("pyproject.toml", tree, "", "")
	if err != nil {
		log.Printf("Failed to find pyproject.toml files: %v", err)
	} else {
		good = true
		foundFiles = append(foundFiles, foundPyprojFiles...)
	}

	var verifiedFiles []utils.FileVerification

	for _, foundFile := range foundFiles {
		if foundFile.Filetype == "pyproject.toml" {
			verification, err := pyprojecttoml.VerifyPyProjectFile(ctx, foundFile, name, version)
			if err != nil {
				log.Printf("Failed to verify pyproject.toml file: %v", err)
				continue
			}
			verifiedFiles = append(verifiedFiles, verification)
		} else {
			log.Printf("Unsupported file type for verification: %s", foundFile.Filetype)
		}
	}

	if !good {
		return nil, errors.New("no supported build files found for requirement extraction")
	}

	if len(verifiedFiles) == 0 {
		return nil, errors.New("no verified build files found for requirement extraction")
	}

	sortedVerification := utils.SortVerifications(verifiedFiles)

	bestFile := sortedVerification[0]
	dir := bestFile.Path

	posFiles := []utils.FoundFile{bestFile.FoundF}
	for _, f := range foundFiles {
		if f.Path == dir && f.Filename != bestFile.FoundF.Filename {
			posFiles = append(posFiles, f)
		}
	}

	for _, f := range posFiles {
		if f.Filetype == "pyproject.toml" {
			pyprojReqs, err := pyprojecttoml.ExtractPyProjectRequirements(ctx, f.FileObject)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to extract pyproject.toml requirements")
			}

			reqs = append(reqs, pyprojReqs...)
		}
	}

	return reqs, nil
}
