// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import "google.golang.org/genai"

var WrongToolchain = &genai.Part{
	Text: `The category of the issue is "Wrong Toolchain".
This happens because the build logs complain 
The logs can convey this error is multiple ways.
1) "Cannot find a Java installation on your machine matching "
2) "No matching toolchains found for requested specification"
3) "Java compilation initialization error invalid source/target release:"
4) "requires Java X to run. You are currently using Java Y."
5) maven-toolchains-plugin errors "Cannot find matching toolchain definitions"
6) "Detected JDK Version: X is not in the allowed range [Y,)."
7) "Source option 5 is no longer supported. Use 6 or later." This happens because the build is using an old Java version that is no longer supported by Java version used in the build logs.`,
}

var WrongToolchainFix = &genai.Part{
	Text: `To determine the required JDK version from an error message, look for specific phrases.
If the message contains "{languageVersion=XX}" or "Android Gradle plugin requires Java XX to run", you need exactly version XX.
If the error includes "invalid source release: XX", "invalid target release: XX", "Dependency requires at least JVM runtime version XX", "...is not in the allowed range [XX,)", or "JDK XX or higher is required...", you must use at least version XX.
For messages with text like "...jdk [ version='...' ]", the required version is the number or range specified within the quotes (e.g., '1.8' or '[11,)').
Lastly, an error like "class file version 61.0" indicates a newer JDK is required, as version 61.0 specifically corresponds to Java 17.`,
}

var ArtifactNotFound = &genai.Part{
	Text: `The category of the issue is "Artifact Not Found".
It happens when the build succeeds but at the we try to get the build artifacts and we get a "cp: cannot stat" error or "No such file or directory" error.
If you see "chmod: cannot access" error with build success, it is the same issue and classify it as "Artifact Not Found".`,
}

var ArtifactNotFoundFix = &genai.Part{
	Text: `To fix the "Artifact Not Found" issue, you need to identify the correct location of the build artifacts.
Use a find command in the strategy to search for *.jar and then copy them to the /out directory.`,
}
