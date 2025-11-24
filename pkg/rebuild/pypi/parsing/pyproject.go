package parsing

import (
	"context"
	"log"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

type ProjectMetadata struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

type ToolMetadata struct {
	Poetry ProjectMetadata `toml:"poetry"`
}

type PyProjectProject struct {
	Metadata ProjectMetadata `toml:"project"`
	Tool     ToolMetadata    `toml:"tool"`
}

func VerifyPyProjectFile(ctx context.Context, foundFile FoundFile, name, version string) (FileVerification, error) {
	var verificationResult FileVerification
	verificationResult.FoundF = foundFile
	verificationResult.Name = name
	verificationResult.Type = foundFile.Filetype
	verificationResult.Path = foundFile.Path
	f := foundFile.FileObject

	pyprojContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to read pyproject.toml")
	}
	var pyProject PyProjectProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return verificationResult, errors.Wrap(err, "Failed to decode pyproject.toml")
	}
	foundName := ""
	foundVersion := ""
	if pyProject.Metadata.Name != "" {
		foundName = pyProject.Metadata.Name
		foundVersion = pyProject.Metadata.Version
	} else if pyProject.Tool.Poetry.Name != "" {
		foundName = pyProject.Tool.Poetry.Name
		foundVersion = pyProject.Tool.Poetry.Version
	}

	if foundFile.Path == "." {
		verificationResult.Main = true
	}

	if foundName != "" {
		editDist := MinEditDistance(NormalizeName(name), NormalizeName(foundName))
		verificationResult.LevDistance = editDist

		if editDist == 0 {
			verificationResult.NameMatch = true

			if foundVersion != "" && version == foundVersion {
				verificationResult.VersionMatch = true
			}
		} else {
			verificationResult.PartialNameMatch = true

			if foundVersion != "" && version == foundVersion {
				verificationResult.PartialVersionMatch = true
			}
		}
	}

	return verificationResult, nil
}

func ExtractPyProjectRequirements(ctx context.Context, f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in pyproject.toml")
	pyprojContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read pyproject.toml")
	}
	type BuildSystem struct {
		Requirements []string `toml:"requires"`
	}
	type PyProject struct {
		Build BuildSystem `toml:"build-system"`
	}
	var pyProject PyProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return nil, errors.Wrap(err, "Failed to decode pyproject.toml")
	}
	for _, r := range pyProject.Build.Requirements {
		// TODO: Some of these requirements are probably already in rbcfg.Requirements, should we skip
		// them? To even know which package we're looking at would require parsing the dependency spec.
		// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#dependency-specifiers
		reqs = append(reqs, strings.ReplaceAll(r, " ", ""))
	}
	log.Println("Added these reqs from pyproject.toml: " + strings.Join(reqs, ", "))
	return reqs, nil
}
