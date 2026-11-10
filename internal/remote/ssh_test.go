package remote

import (
	"errors"
	"testing"
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
