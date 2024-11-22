package docker

import (
	re "regexp"

	"github.com/google/oss-rebuild/internal/proxy/dockerfs"
	"github.com/pkg/errors"
)

var distroPattern = re.MustCompile(`\bID=["'']?([^\r\n]+?)["'']?[\r\n]`)

// distro returns the given container's distribution identifier.
func distro(dfs *dockerfs.Filesystem) (string, error) {
	_, err := dfs.Stat("/kaniko")
	if err == nil {
		return "kaniko", nil
	}
	f, err := dfs.OpenAndResolve("/etc/os-release")
	if err != nil {
		return "", err
	}
	matches := distroPattern.FindSubmatch(f.Contents)
	if matches == nil {
		return "", errors.New("distro identifier not found")
	}
	return string(matches[1]), nil
}

// locateTruststore returns the truststore file for the given OS distribution.
func locateTruststore(dfs *dockerfs.Filesystem) (*dockerfs.File, error) {
	d, err := distro(dfs)
	if err != nil {
		return nil, err
	}
	switch d {
	case "alpine", "arch", "openwrt":
		// Expected Cert File: /etc/ssl/cert.pem
		// Expected Cert Dir:  /etc/ssl/certs/
		return dfs.OpenAndResolve("/etc/ssl/cert.pem")
	case "rhel", "fedora", "centos":
		// Expected Cert File: /etc/pki/tls/cert.pem
		// Expected Cert Dir:  /etc/pki/tls/certs
		return dfs.OpenAndResolve("/etc/pki/tls/cert.pem")
	case "debian", "ubuntu", "gentoo", "linuxmint":
		// Expected Cert File: /etc/ssl/certs/ca-certificates.crt
		// Expected Cert Dir:  /etc/ssl/certs/
		// NOTE: Only expected to be present if ca-certificates installed or is distroless
		// NOTE: To survive regeneration, cert needs to be added to /usr/share/ca-certificates/
		// and the new relpath added to a new line in /etc/ca-certificates.conf.
		return dfs.OpenAndResolve("/etc/ssl/certs/ca-certificates.crt")
	case "opensuse-leap", "opensuse-tumbleweed":
		// Expected Cert File: /var/lib/ca-certificates/ca-bundle.pem
		// Expected Cert Dir:  /var/lib/ca-certificates/{openssl,pem}/
		// NOTE: JKS file also needs to be regenerated at /var/lib/ca-certificates/java-cacerts.
		return dfs.OpenAndResolve("/var/lib/ca-certificates/ca-bundle.pem")
	case "kaniko":
		return dfs.OpenAndResolve("/kaniko/ssl/certs/ca-certificates.crt")
	default:
		return nil, errors.Errorf("unsupported distro: %s", d)
	}
}
