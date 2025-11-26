package parsing

import (
	"log"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/ini"
	"github.com/pkg/errors"
)

func parseMultiLineValue(value string) []string {
	// cfg specification in this doc: https://setuptools.pypa.io/en/latest/userguide/declarative_config.html
	// setup_requires may be list-semi (list separated by a semi-colon) or dangling list (newline seperated)
	lines := strings.Split(value, "\n")
	if len(lines) == 1 {
		// Try a semi-colon as a separator if no newlines or commas are found
		lines = strings.Split(value, ";")
	}

	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func verifySetupCfgFile(foundFile FoundFile, name, version string) (FileVerification, error) {
	var verificationResult FileVerification
	verificationResult.FoundF = foundFile
	verificationResult.Name = name
	verificationResult.Type = foundFile.Filetype
	verificationResult.Path = foundFile.Path
	f := foundFile.FileObject

	cfgContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to read setup.py")
	}

	cfgReader := strings.NewReader(cfgContents)

	cfg, err := ini.Parse(cfgReader)
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to parse setup.cfg")
	}

	foundName, fn := cfg.GetValue("metadata", "name")
	foundVersion, fv := cfg.GetValue("metadata", "version")

	if foundFile.Path == "." {
		verificationResult.Main = true
	}

	if fn {
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName))
		verificationResult.LevDistance = editDist

		if editDist == 0 {
			verificationResult.NameMatch = true

			if fv && version == foundVersion {
				verificationResult.VersionMatch = true
			}
		} else {
			verificationResult.PartialNameMatch = true

			if fv && version == foundVersion {
				verificationResult.PartialVersionMatch = true
			}
		}
	}

	return verificationResult, nil
}

func extractSetupCfgRequirements(f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in setup.cfg")
	cfgContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read setup.cfg")
	}

	cfgReader := strings.NewReader(cfgContents)

	cfg, err := ini.Parse(cfgReader)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to parse setup.cfg")
	}

	setupRequires, found := cfg.GetValue("options", "setup_requires")
	if found {
		setupRequiresList := parseMultiLineValue(setupRequires)
		reqs = append(reqs, setupRequiresList...)
	}

	log.Println("Added these reqs from setup.cfg: " + strings.Join(reqs, ", "))
	return reqs, nil
}
