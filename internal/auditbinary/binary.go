// Package auditbinary carries the standalone agent executables prepared at build time.
// The LLM client remains a separate process with its own configuration and credentials.
package auditbinary

import (
	"embed"
	"fmt"
	"runtime"
)

//go:generate go run generate.go
//go:embed binaries/*
var binaries embed.FS

func Read(goos, arch string) ([]byte, error) {
	name := goos + "-" + arch
	if goos == "windows" {
		name += ".exe"
	}
	b, e := binaries.ReadFile("binaries/" + name)
	if e != nil {
		return nil, fmt.Errorf("audit-agent binary %s is not packaged; run go generate ./internal/auditbinary before building: %w", name, e)
	}
	return b, nil
}
func Native() ([]byte, error) { return Read(runtime.GOOS, runtime.GOARCH) }
