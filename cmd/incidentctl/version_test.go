package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/sshproxy"
)

func TestVersionDoesNotLoadConfigurationOrProxy(t *testing.T) {
	t.Setenv(sshproxy.Environment, "invalid-proxy-setting")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stdout := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = stdout; writer.Close() }()
	code := run([]string{"--version", "-config", filepath.Join(t.TempDir(), "missing.json")})
	writer.Close()
	os.Stdout = stdout
	data, err := io.ReadAll(reader)
	if err != nil || code != 0 || string(data) != versionText() || !strings.Contains(string(data), "audit-agent:") {
		t.Fatalf("version without config: code %d, output %q, error %v", code, data, err)
	}
}
