package parsing

import (
	"log"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

func verifySetupPyFile(foundFile FoundFile, name, version string) (FileVerification, error) {
	var verificationResult FileVerification
	verificationResult.FoundF = foundFile
	verificationResult.Name = name
	verificationResult.Type = foundFile.Filetype
	verificationResult.Path = foundFile.Path
	f := foundFile.FileObject

	setupPyContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to read setup.py")
	}

	setupPyFunctionArgs := gatherSetupPyData(name, []byte(setupPyContents))
	for _, call := range setupPyFunctionArgs.setupCalls {
		if nameVal, ok := call.arguments.keywordArgs["name"]; ok {
			if nameVal.typ == "string" {
				editDist := minEditDistance(normalizeName(name), normalizeName(nameVal.value.(string)))
				verificationResult.LevDistance = editDist

				if editDist == 0 {
					verificationResult.NameMatch = true

					if versionVal, vok := call.arguments.keywordArgs["version"]; vok {
						if versionVal.typ == "string" && versionVal.value.(string) == version {
							verificationResult.VersionMatch = true
						}
					}
				} else {
					verificationResult.PartialNameMatch = true

					if versionVal, vok := call.arguments.keywordArgs["version"]; vok {
						if versionVal.typ == "string" && versionVal.value.(string) == version {
							verificationResult.PartialVersionMatch = true
						}
					}
				}
			}
		}
	}

	return verificationResult, nil
}

func extractSetupPyRequirements(f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in setup.py")
	setupPyContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read setup.py")
	}

	setupPyFunctionArgs := gatherSetupPyData("Doesn't matter", []byte(setupPyContents))
	for _, call := range setupPyFunctionArgs.setupCalls {
		if extractedSetupReqs, ok := call.arguments.keywordArgs["setup_requires"]; ok {
			switch extractedSetupReqs.typ {
			case "list":
				for _, v := range extractedSetupReqs.value.([]extractedValue) {
					if v.typ == "string" {
						reqs = append(reqs, v.value.(string))
					} else {
						log.Printf("setup_requires contained a non-string value of type %s", v.typ)
					}
				}
			case "string":
				reqs = append(reqs, extractedSetupReqs.value.(string))
			default:
				log.Printf("setup_requires is of unsupported type %s", extractedSetupReqs.typ)
			}
		}
	}

	log.Println("Added these reqs from setup.py: " + strings.Join(reqs, ", "))
	return reqs, nil
}
