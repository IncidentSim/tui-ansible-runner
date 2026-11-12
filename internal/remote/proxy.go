package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"incidentctl/internal/sshproxy"
)

// ProxyOption uses the incidentctl executable itself as OpenSSH's stdio proxy.
func ProxyOption() (string, error) {
	value := os.Getenv(sshproxy.Environment)
	if value == "" || value == "direct" {
		return "", nil
	}
	proxy, err := sshproxy.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", sshproxy.Environment, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return proxyOption(filepath.ToSlash(executable), proxy.String(), runtime.GOOS), nil
}

func proxyOption(executable, proxy, goos string) string {
	quote := shellQuote
	if goos == "windows" {
		quote = func(s string) string { return `"` + s + `"` }
	}
	// OpenSSH expands percent tokens before starting the command's shell.
	executable = strings.ReplaceAll(executable, "%", "%%")
	return "ProxyCommand=" + quote(executable) + " ssh-proxy --url " + quote(proxy) + " --host %h --port %p"
}
