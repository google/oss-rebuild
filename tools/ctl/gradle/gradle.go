// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gradle

import (
	"context"
	"encoding/json"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/pkg/build/local"
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
                        group: project.getGroup().toString(),
                        artifact: project.getName(),
                        version: project.getVersion().toString(),
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
	Group         string          `json:"group"`
	Artifact      string          `json:"artifact"`
	Version       string          `json:"version"`
	BuildManifest string          `json:"buildManifest"`
	Submodules    []GradleProject `json:"submodules,omitempty"`
}

// RunPrintCoordinates runs the gradle script which print all possible GAV coordinates of a Gradle project and its submodules.
// We run the script without a daemon as this is a one-off task and we do not want to keep a background process around.
func RunPrintCoordinates(ctx context.Context, sourceRepo git.Repository, gradleCmdExecutor local.CommandExecutor) (*GradleProject, error) {
	wt, err := sourceRepo.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "getting worktree of Gradle project")
	}
	gradleScriptFile, err := wt.Filesystem.Create("printCoordinates.gradle")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create printCoordinates.gradle")
	}
	if _, err := gradleScriptFile.Write([]byte(gradleScript)); err != nil {
		return nil, errors.Wrap(err, "failed to write printCoordinates.gradle file")
	}
	gradleScriptFile.Close()
	// TODO: make command options a parameter to this function in order to allow user to control the output destination - stdout or file
	err = gradleCmdExecutor.Execute(ctx, local.CommandOptions{Dir: wt.Filesystem.Root(), Output: os.Stdout}, "./gradlew", "printCoordinates", "--init-script", "printCoordinates.gradle", "--no-daemon")
	if err != nil {
		return nil, errors.Wrap(err, "failed to run Gradle command")
	}
	f, err := wt.Filesystem.Open("output.json")
	if err != nil {
		return nil, errors.Wrap(err, "failed to open generated JSON file")
	}
	defer f.Close()
	var gradleProject GradleProject
	err = json.NewDecoder(f).Decode(&gradleProject)
	if err != nil {
		return nil, errors.Wrap(err, "failed to decode generated JSON file")
	}
	return &gradleProject, nil
}
