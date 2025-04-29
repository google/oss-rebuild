// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	re "regexp"

	"github.com/pkg/errors"
)

type truststoreFS interface {
	Exists(path string) bool
	Read(path string) ([]byte, error)
}

var distroPattern = re.MustCompile(`\bID=["'']?([^\r\n]+?)["'']?[\r\n]`)

// distro returns the given container's distribution identifier.
func distro(dfs truststoreFS) (string, error) {
	// Kaniko images are built from scratch and do not have the /etc/os-release file.
	// The /kaniko directory is present in all Kaniko images and also contains its own trust store.
	if dfs.Exists("/kaniko") {
		return "kaniko", nil
	}
	contents, err := dfs.Read("/etc/os-release")
	if err != nil {
		return "", err
	}
	matches := distroPattern.FindSubmatch(contents)
	if matches == nil {
		return "", errors.New("distro identifier not found")
	}
	return string(matches[1]), nil
}

func TruststorePath(dfs truststoreFS) (string, error) {
	d, err := distro(dfs)
	if err != nil {
		return "", err
	}
	switch d {
	case "alpine", "arch", "openwrt":
		// Expected Cert File: /etc/ssl/cert.pem
		// Expected Cert Dir:  /etc/ssl/certs/
		return "/etc/ssl/cert.pem", nil
	case "rhel", "fedora", "centos":
		// Expected Cert File: /etc/pki/tls/cert.pem
		// Expected Cert Dir:  /etc/pki/tls/certs
		return "/etc/pki/tls/cert.pem", nil
	case "debian", "ubuntu", "gentoo", "linuxmint":
		// Expected Cert File: /etc/ssl/certs/ca-certificates.crt
		// Expected Cert Dir:  /etc/ssl/certs/
		// NOTE: Only expected to be present if ca-certificates installed or is distroless
		// NOTE: To survive regeneration, cert needs to be added to /usr/share/ca-certificates/
		// and the new relpath added to a new line in /etc/ca-certificates.conf.
		return "/etc/ssl/certs/ca-certificates.crt", nil
	case "opensuse-leap", "opensuse-tumbleweed":
		// Expected Cert File: /var/lib/ca-certificates/ca-bundle.pem
		// Expected Cert Dir:  /var/lib/ca-certificates/{openssl,pem}/
		// NOTE: JKS file also needs to be regenerated at /var/lib/ca-certificates/java-cacerts.
		return "/var/lib/ca-certificates/ca-bundle.pem", nil
	case "kaniko":
		// Expected Cert File: /kaniko/ssl/certs/ca-certificates.crt
		// Expected Cert Dir: /kaniko/ssl/certs
		// https://github.com/GoogleContainerTools/kaniko/blob/e328007bc1fa0d8c2eacf1918bebbabc923abafa/deploy/Dockerfile#L69
		return "/kaniko/ssl/certs/ca-certificates.crt", nil
	default:
		return "", errors.Errorf("unsupported distro: %s", d)
	}
}
