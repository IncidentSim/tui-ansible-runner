//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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
		fmt.Println("Building audit-agent", name)
		if e = auditbuild.Build(source, output, p[0], p[1]); e != nil {
			return e
		}
	}
	return nil
}
