package remote

import (
	"strings"
	"testing"

	"incidentctl/internal/sshproxy"
	"incidentctl/internal/vm"
)

func TestProxyCommandUsesPackagedHelper(t *testing.T) {
	t.Setenv(sshproxy.Environment, "http://127.0.0.1:10809")
	cmd := New(vm.Connection{Host: "vm.example", User: "root", Port: 22000}).command("run-once")
	args := strings.Join(cmd.Args, " ")
	for _, value := range []string{"ProxyCommand=", "ssh-proxy", "http://127.0.0.1:10809", "--host %h --port %p", "root@vm.example"} {
		if !strings.Contains(args, value) {
			t.Fatalf("missing %s in %s", value, args)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "run-once" {
		t.Fatal("proxy changed the remote command")
	}
	t.Setenv(sshproxy.Environment, "direct")
	if option, err := ProxyOption(); err != nil || option != "" {
		t.Fatalf("direct mode: %s %v", option, err)
	}
}

func TestInvalidProxyFailsInsteadOfConnectingDirectly(t *testing.T) {
	t.Setenv(sshproxy.Environment, "ftp://proxy.invalid")
	err := New(vm.Connection{Host: "vm.example"}).command("true").Run()
	if err == nil || !strings.Contains(err.Error(), sshproxy.Environment) {
		t.Fatalf("expected invalid proxy error, got %v", err)
	}
}

func TestProxyCommandQuotesExecutableAndEscapesSSHTokens(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		option := proxyOption("C:/User files/100%/incidentctl", "http://localhost:10809", goos)
		if !strings.Contains(option, "100%%") || !strings.Contains(option, "--host %h --port %p") {
			t.Fatal(option)
		}
		if goos == "windows" && !strings.Contains(option, `"C:/User files/100%%/incidentctl"`) {
			t.Fatal(option)
		}
		if goos != "windows" && !strings.Contains(option, "'C:/User files/100%%/incidentctl'") {
			t.Fatal(option)
		}
	}
}
