package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"incidentctl/internal/vm"
)

// fakeSSH installs an executable named ssh first in PATH. It stands in for the
// real transport so the remote boundary is exercised without a VM.
func fakeSSH(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testClient(out *bytes.Buffer) *Client {
	return NewWithWriters(vm.Connection{Host: "vm.example", User: "root", Port: 22}, out, out)
}

func TestContextCommandsReturnOutput(t *testing.T) {
	fakeSSH(t, `cat >/dev/null; echo hello`)
	var buf bytes.Buffer
	got, err := testClient(&buf).RunScriptOutputContext(context.Background(), "true")
	if err != nil || got != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestContextCancellationStopsSSH(t *testing.T) {
	fakeSSH(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	start := time.Now()
	err := testClient(&buf).RunScriptContext(ctx, "true")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if IsCommandError(err) || IsTransportError(err) {
		t.Fatalf("a cancelled context must not look like a remote result: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("ssh was not terminated on cancellation")
	}
}

func TestTransportErrorIsDistinctFromRemoteExit(t *testing.T) {
	fakeSSH(t, `exit 255`)
	var buf bytes.Buffer
	err := testClient(&buf).RunScriptContext(context.Background(), "true")
	if !IsTransportError(err) {
		t.Fatalf("exit 255 must be a transport error: %v", err)
	}
	fakeSSH(t, `exit 2`)
	err = testClient(&buf).RunScriptContext(context.Background(), "true")
	if IsTransportError(err) || !IsCommandError(err) {
		t.Fatalf("exit 2 must be a remote command result: %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatal(err)
	}
}

func TestIsTransportErrorForStartFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var buf bytes.Buffer
	err := testClient(&buf).RunScriptContext(context.Background(), "true")
	if err == nil || !IsTransportError(err) {
		t.Fatalf("failure to start ssh is a transport failure: %v", err)
	}
}
