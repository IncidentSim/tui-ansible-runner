//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	source := os.Getenv("AUDIT_AGENT_DIR")
	if source == "" {
		source = "../../../audit-agent"
	}
	source, e := filepath.Abs(source)
	if e != nil {
		panic(e)
	}
	if _, e = os.Stat(filepath.Join(source, "go.mod")); e != nil {
		panic("set AUDIT_AGENT_DIR to the Go audit-agent source checkout: " + e.Error())
	}
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
			panic(e)
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/incident-audit")
		cmd.Dir = source
		cmd.Env = []string{}
		for _, v := range os.Environ() {
			k, _, _ := strings.Cut(v, "=")
			if k != "GOOS" && k != "GOARCH" && k != "CGO_ENABLED" {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "GOOS="+p[0], "GOARCH="+p[1], "CGO_ENABLED=0")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Println("Building audit-agent", name)
		if e = cmd.Run(); e != nil {
			panic(e)
		}
	}
}
