//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"incidentctl/internal/auditbuild"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "prepare audit-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	source, e := auditbuild.Source(os.Getenv("AUDIT_AGENT_DIR"), os.Getenv("AUDIT_AGENT_REPOSITORY"), os.Getenv("AUDIT_AGENT_REF"), "../../.cache/audit-agent")
	if e != nil {
		return e
	}
	fmt.Println("audit-agent source:", source)
	goos, arch := os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	platforms := [][2]string{{"linux", "amd64"}, {goos, arch}}
	built := map[string]bool{}
	for _, p := range platforms {
		name := p[0] + "-" + p[1]
		if built[name] {
			continue
		}
		built[name] = true
		if p[0] == "windows" {
			name += ".exe"
		}
		output, e := filepath.Abs(filepath.Join("binaries", name))
		if e != nil {
			return e
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/incident-audit")
		cmd.Dir = source
		cmd.Env = []string{}
		for _, v := range os.Environ() {
			k, _, _ := strings.Cut(v, "=")
			if !strings.EqualFold(k, "GOOS") && !strings.EqualFold(k, "GOARCH") && !strings.EqualFold(k, "CGO_ENABLED") && !strings.EqualFold(k, "GOWORK") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "GOOS="+p[0], "GOARCH="+p[1], "CGO_ENABLED=0", "GOWORK=off")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Println("Building audit-agent", name)
		if e = cmd.Run(); e != nil {
			return fmt.Errorf("build audit-agent %s: %w", name, e)
		}
	}
	return nil
}
