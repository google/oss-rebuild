package manifest

import (
	"encoding/xml"
	"regexp"
)

type Build struct {
	Plugins struct {
		Plugin []struct {
			ArtifactID    string `xml:"artifactId"`
			GroupID       string `xml:"groupId"`
			Configuration struct {
				// This is the property that contains the name of the git properties file.
				GenerateGitPropertiesFilename string `xml:"generateGitPropertiesFilename"`
			} `xml:"configuration"`
		} `xml:"plugin"`
	} `xml:"plugins"`
}

type Project struct {
	Build Build `xml:"build"`

	Profiles struct {
		Profile []struct {
			Build Build `xml:"build"`
		} `xml:"profile"`
	} `xml:"profiles"`
}

func ParsePom(pom []byte) Project {
	mavenProject := Project{}
	err := xml.Unmarshal(pom, &mavenProject)
	if err != nil {
		panic("Incorrect XML format in pom.xml")
	}
	return mavenProject
}

func ParsePomProperties(property string) string {
	// Usually, no one overrides the default properties of the maven build.
	// If they do, we would need to parse the properties in POM file.
	defaultProperties := map[string]string{
		"project.build.directory":       "",
		"project.build.outputDirectory": "classes",
	}
	propertyRegex := regexp.MustCompile(`\$\{[^}]+\}/?`)
	return propertyRegex.ReplaceAllString(property, defaultProperties[property])
}
