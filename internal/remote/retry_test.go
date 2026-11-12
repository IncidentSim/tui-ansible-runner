package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"incidentctl/internal/vm"
)

func TestSSHRetryProcess(t *testing.T) {
	if os.Getenv("INCIDENTCTL_SSH_RETRY_CHILD") != "1" {
		return
	}
	dir := os.Getenv("INCIDENTCTL_SSH_RETRY_DIR")
	before, _ := os.ReadFile(filepath.Join(dir, "attempts"))
	attempt := len(before) + 1
	if err := os.WriteFile(filepath.Join(dir, "attempts"), append(before, 'x'), 0600); err != nil {
		panic(err)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("input-%d", attempt)), input, 0600); err != nil {
		panic(err)
	}
	mode := os.Getenv("INCIDENTCTL_SSH_RETRY_MODE")
	log, status := "", 0
	switch mode {
	case "recover":
		if attempt < 3 {
			log, status = "Connection timed out during banner exchange\nConnection to UNKNOWN port 65535 timed out\n", 255
		} else {
			log = "Authenticated to vm.example using publickey\ndebug1: Sending command: run-once\n"
			os.WriteFile(filepath.Join(dir, "executions"), []byte("x"), 0600)
			fmt.Print("remote-result")
		}
	case "exhausted":
		log, status = "Connection timed out during banner exchange\n", 255
	case "after-auth":
		log, status = "Authenticated to vm.example using publickey\ndebug1: Sending command: run-once\nclient_loop: send disconnect: Broken pipe\n", 255
		fmt.Fprintln(os.Stderr, "Connection timed out during banner exchange")
	case "remote-stderr":
		status = 255
		fmt.Fprintln(os.Stderr, "Connection timed out during banner exchange")
	case "playbook-error":
		log, status = "Authenticated to vm.example using publickey\n", 2
	case "proxy-rejected":
		log, status = "kex_exchange_identification: Connection closed by remote host\n", 255
		fmt.Fprintln(os.Stderr, "SSH proxy tunnel: HTTP CONNECT rejected (status 403)")
	default:
		panic("unknown retry helper mode")
	}
	if err := os.WriteFile(os.Getenv("INCIDENTCTL_SSH_RETRY_LOG"), []byte(log), 0600); err != nil {
		panic(err)
	}
	os.Exit(status)
}

func retryFixture(t *testing.T, mode string) (*Client, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	client := NewWithWriters(vm.Connection{Host: "vm.example", Port: 22000, User: "root"}, &output, &diagnostics)
	client.retrySleep = func(time.Duration) {}
	client.attemptCommand = func(command, log string) *exec.Cmd {
		cmd := exec.Command(executable, "-test.run=^TestSSHRetryProcess$")
		cmd.Env = append(os.Environ(), "INCIDENTCTL_SSH_RETRY_CHILD=1", "INCIDENTCTL_SSH_RETRY_DIR="+dir, "INCIDENTCTL_SSH_RETRY_MODE="+mode, "INCIDENTCTL_SSH_RETRY_LOG="+log)
		return cmd
	}
	return client, dir, &output, &diagnostics
}

func TestSSHBannerFailuresRetryWithoutRepeatingCommand(t *testing.T) {
	client, dir, _, diagnostics := retryFixture(t, "recover")
	output, err := client.RunScriptOutput("printf run-once")
	if err != nil || output != "remote-result" {
		t.Fatalf("output %q: %v", output, err)
	}
	attempts, _ := os.ReadFile(filepath.Join(dir, "attempts"))
	executions, _ := os.ReadFile(filepath.Join(dir, "executions"))
	if len(attempts) != 3 || len(executions) != 1 || strings.Count(diagnostics.String(), "повтор") != 2 {
		t.Fatalf("attempts %d, executions %d, diagnostics %s", len(attempts), len(executions), diagnostics)
	}
	for i := 1; i <= 3; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "input-"+strconv.Itoa(i)))
		if err != nil || string(data) != "printf run-once" {
			t.Fatalf("input changed on attempt %d: %q %v", i, data, err)
		}
	}
}

func TestSSHUploadRetriesReplayNonSeekableInput(t *testing.T) {
	client, dir, _, _ := retryFixture(t, "recover")
	input := struct{ io.Reader }{strings.NewReader("archive-data")}
	if err := client.RunWithInput(input, "tar", "-xf", "-"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "input-"+strconv.Itoa(i)))
		if err != nil || string(data) != "archive-data" {
			t.Fatalf("upload input on attempt %d: %q %v", i, data, err)
		}
	}
}

func TestSSHSeekableUploadPreservesStartingOffset(t *testing.T) {
	client, dir, _, _ := retryFixture(t, "recover")
	input := strings.NewReader("prefix-payload")
	input.Seek(7, io.SeekStart)
	if err := client.RunWithInput(input, "cat"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		data, _ := os.ReadFile(filepath.Join(dir, "input-"+strconv.Itoa(i)))
		if string(data) != "payload" {
			t.Fatalf("upload reset caller's offset: %q", data)
		}
	}
}

func TestSSHUnsafeFailuresAreNeverRetried(t *testing.T) {
	for _, mode := range []string{"after-auth", "remote-stderr", "playbook-error", "proxy-rejected"} {
		t.Run(mode, func(t *testing.T) {
			client, dir, _, _ := retryFixture(t, mode)
			if err := client.RunScript("run-once"); err == nil {
				t.Fatal("expected failure")
			}
			attempts, _ := os.ReadFile(filepath.Join(dir, "attempts"))
			if len(attempts) != 1 {
				t.Fatalf("unsafe command replay: %d attempts", len(attempts))
			}
		})
	}
}

func TestSSHBannerRetriesAreBoundedAndReportDestination(t *testing.T) {
	client, dir, _, _ := retryFixture(t, "exhausted")
	err := client.RunScript("run-once")
	var exit *CommandError
	if !errors.As(err, &exit) || exit.ExitCode != 255 || !exit.BeforeCommand || exit.Attempts != 3 || !strings.Contains(err.Error(), "vm.example:22000") {
		t.Fatalf("wrong final error: %v", err)
	}
	attempts, _ := os.ReadFile(filepath.Join(dir, "attempts"))
	if len(attempts) != 3 {
		t.Fatalf("unbounded attempts: %d", len(attempts))
	}
}

func TestRetryClassificationFailsClosed(t *testing.T) {
	if !retryBeforeCommand("banner exchange: Connection to UNKNOWN port 65535: Unknown error\n", "", 0) {
		t.Fatal("Windows OpenSSH banner disconnect must be retryable")
	}
	for _, log := range []string{
		"debug1: Remote: Connection timed out during banner exchange\n",
		"Authenticated to vm.example\nConnection timed out during banner exchange\n",
		"debug1: Sending command: run-once\nConnection timed out during banner exchange\n",
		"debug1: mux_client_request_session\nConnection timed out during banner exchange\n",
	} {
		if retryBeforeCommand(log, "", 0) {
			t.Fatalf("unsafe retry for log %q", log)
		}
	}
	if retryBeforeCommand("Connection timed out during banner exchange\n", "", 1) {
		t.Fatal("command with output was replayed")
	}
}
