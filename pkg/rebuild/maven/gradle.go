// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"encoding/json"
	"os"
	"os/exec"
	"path"

	"github.com/go-git/go-git/v5"
	"github.com/pkg/errors"
)

const gradleScript = `import groovy.json.JsonOutput

gradle.projectsLoaded {
    def outputJsonPath = gradle.startParameter.projectProperties['outputJson']

    gradle.rootProject.tasks.register("printCoordinates") {
        doLast {
            def jsonFile = new File(outputJsonPath ?: "output.json")

            def rootProjectPath = gradle.rootProject.getProjectDir().toPath()

            def toJsonMap
            toJsonMap = { Project project ->
                def map = [
                        groupId   : project.getGroup().toString(),
                        artifactId: project.getName(),
                        version   : project.getVersion().toString(),
                        buildManifest: rootProjectPath.relativize(project.getBuildFile().toPath()).toString(),
                ]
                if (!project.getChildProjects().isEmpty()) {
                    map.submodules = project.getChildProjects().values().collect {
                        toJsonMap(it)
                    }
                }
                return map
            }

            def jsonModel = toJsonMap(gradle.getRootProject())
            def jsonString = JsonOutput.prettyPrint(JsonOutput.toJson(jsonModel))
            jsonFile.text = jsonString
        }
    }
}`

type GradleProject struct {
	GroupId       string          `json:"groupId"`
	ArtifactId    string          `json:"artifactId"`
	Version       string          `json:"version"`
	BuildFilePath string          `json:"buildFilePath"`
	Submodules    []GradleProject `json:"submodules,omitempty"`
}

func RunPrintCoordinates(sourceRepo git.Repository) (GradleProject, error) {
	cmd := exec.Command("./gradlew", "printCoordinates", "--init-script", "printCoordinates.gradle")
	wt, err := sourceRepo.Worktree()
	if err != nil {
		return GradleProject{}, errors.Wrap(err, "getting worktree of Gradle project")
	}
	rootDir := wt.Filesystem.Root()
	cmd.Dir = rootDir
	if err := os.WriteFile(path.Join(rootDir, "printCoordinates.gradle"), []byte(gradleScript), 0644); err != nil {
		return GradleProject{}, errors.Wrap(err, "failed to write print_coordinates.gradle file")
	}
	err = cmd.Run()
	if err != nil {
		return GradleProject{}, errors.Wrap(err, "failed to run Gradle command")
	}
	pathToGeneratedJson := path.Join(wt.Filesystem.Root(), "output.json")
	f, err := os.Open(pathToGeneratedJson)
	if err != nil {
		return GradleProject{}, errors.Wrap(err, "failed to open generated JSON file")
	}
	defer f.Close()
	var gradleProject GradleProject
	err = json.NewDecoder(f).Decode(&gradleProject)
	if err != nil {
		return GradleProject{}, errors.Wrap(err, "failed to decode generated JSON file")
	}
	return gradleProject, nil
}
