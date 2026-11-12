package remote

import (
	"errors"
	"strings"
	"testing"

	"incidentctl/internal/vm"
)

func TestShellQuote(t *testing.T) {
	t.Parallel()
	got := shellQuote("a'b c")
	want := `'a'"'"'b c'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestIsCommandError(t *testing.T) {
	t.Parallel()
	err := errors.New("transport")
	if IsCommandError(err) {
		t.Fatal("transport error must not be treated as a remote exit status")
	}
	if !IsCommandError(&CommandError{ExitCode: 2}) {
		t.Fatal("expected command error to be recognized")
	}
}

func TestSSHCommandUsesSingleTimedConnectionAttempt(t *testing.T) {
	cmd := New(vm.Connection{Host: "vm.example", User: "root", Port: 22000}).command("run-once")
	args := strings.Join(cmd.Args, " ")
	for _, option := range []string{"ConnectTimeout=30", "ConnectionAttempts=1", "ServerAliveInterval=15", "ServerAliveCountMax=2"} {
		if !strings.Contains(args, option) {
			t.Fatalf("missing %s: %s", option, args)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "run-once" {
		t.Fatal("remote command changed")
	}
}
