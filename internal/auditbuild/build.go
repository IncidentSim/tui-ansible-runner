package auditbuild

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Environment builds independent target binaries, including from a Go workspace.
func Environment(goos, arch string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.EqualFold(key, "GOOS") && !strings.EqualFold(key, "GOARCH") && !strings.EqualFold(key, "CGO_ENABLED") && !strings.EqualFold(key, "GOWORK") {
			env = append(env, value)
		}
	}
	return append(env, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0", "GOWORK=off")
}

func Build(source, output, goos, arch string) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/incident-audit")
	cmd.Dir, cmd.Env = source, Environment(goos, arch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build audit-agent %s/%s: %w", goos, arch, err)
	}
	return nil
}
