package main

import (
	"runtime"
	"strings"
	"testing"

	"incidentctl/internal/sshproxy"
	"incidentctl/internal/vm"
)

func TestInteractiveSSHIncludesProxy(t *testing.T) {
	t.Setenv(sshproxy.Environment, "http://127.0.0.1:10809")
	command := sshCommand(vm.Connection{Host: "vm.example", Port: 22000, User: "root"})
	for _, value := range []string{"ProxyCommand=", "ssh-proxy", "http://127.0.0.1:10809", "root@vm.example"} {
		if !strings.Contains(command, value) {
			t.Fatalf("missing %q in %s", value, command)
		}
	}
	if runtime.GOOS == "windows" && !strings.HasPrefix(command, "ssh --% ") {
		t.Fatal("PowerShell must preserve native ProxyCommand quotes")
	}
}
