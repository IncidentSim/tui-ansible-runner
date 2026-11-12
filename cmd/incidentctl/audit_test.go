package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"incidentctl/internal/remote"
)

type fakeAuditClient struct {
	operations []string
	packet     string
	err        error
	startErr   error
	bundle     []byte
}

func (f *fakeAuditClient) RunScriptOutput(script string, args ...string) (string, error) {
	f.operations = append(f.operations, args[2])
	if args[2] == "start" {
		return "", f.startErr
	}
	return f.packet, f.err
}
func (f *fakeAuditClient) RunShellInput(script string, input io.Reader, args ...string) error {
	f.operations = append(f.operations, "install")
	f.bundle, _ = io.ReadAll(input)
	return nil
}

func TestAuditStopFailureBlocksVerification(t *testing.T) {
	for _, client := range []*fakeAuditClient{
		{err: errors.New("stop failed")}, {packet: "not JSON"}, {packet: "{}"},
	} {
		a := auditOptions{ReportsDir: t.TempDir()}
		err := a.verify(client, "/work", "example", "unused", io.Discard, func(io.Writer) error {
			t.Fatal("verification must not run after stop failure")
			return nil
		})
		if err == nil {
			t.Fatal("expected error")
		}
	}
}

func TestAuditSavesFailedValidationAndPreservesError(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`}
	a := auditOptions{ReportsDir: t.TempDir()}
	want := errors.New("incident still active")
	err := a.verify(client, "/work", "example", "unused", io.Discard, func(out io.Writer) error {
		if strings.Join(client.operations, ",") != "stop" {
			t.Fatal("verification ran before stop")
		}
		fmtOutput := "Ansible failure details"
		io.WriteString(out, fmtOutput)
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("lost validation error: %v", err)
	}
	entries, _ := os.ReadDir(a.ReportsDir)
	if len(entries) != 1 {
		t.Fatalf("reports: %v", entries)
	}
	dir := filepath.Join(a.ReportsDir, entries[0].Name())
	packet, err := os.ReadFile(filepath.Join(dir, "llm.json"))
	if err != nil || !bytes.Contains(packet, []byte("attempt-1")) {
		t.Fatalf("packet: %s %v", packet, err)
	}
	validation, err := os.ReadFile(filepath.Join(dir, "validation.json"))
	if err != nil || !bytes.Contains(validation, []byte(`"passed": false`)) || !bytes.Contains(validation, []byte("Ansible failure details")) {
		t.Fatalf("validation: %s %v", validation, err)
	}
}

func TestAuditBundleExcludesLocalSecrets(t *testing.T) {
	data, err := auditBundle(auditTestPackage(t))
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	count := 0
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "llm") || strings.Contains(h.Name, "secret") || strings.Contains(h.Name, "analyze") {
			t.Fatalf("local-only file uploaded: %s", h.Name)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("bundle files: %d", count)
	}
}

func TestAuditPreparationStopsBeforeInstallAndStart(t *testing.T) {
	a := auditOptions{AgentDir: auditTestPackage(t)}
	client := &fakeAuditClient{}
	if err := a.prepare(client, "/work", "example"); err != nil {
		t.Fatal(err)
	}
	if err := a.start(client, "/work", "example"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(client.operations, ","); got != "install,start" {
		t.Fatalf("order: %s", got)
	}
}

func auditTestPackage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"bin/linux-amd64/incident-audit", "config/llm.local.json", ".secrets/deepseek-api-key"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAuditAnalysisRunsOnlyAfterSuccessfulValidation(t *testing.T) {
	a := defaultAuditOptions()
	a.Binary, _ = os.Executable()
	a.ReportsDir = t.TempDir()
	a.LLMReportsDir = t.TempDir()
	a.LLMConfig = "local-profile.json"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Exercise goal"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := a.verify(&fakeAuditClient{packet: `{"session":"attempt-1"}`}, "/work", "example", dir, &output, func(io.Writer) error { return nil })
	if err != nil {
		t.Fatalf("analysis handoff failed: %v %s", err, output.String())
	}
	reports, _ := filepath.Glob(filepath.Join(a.LLMReportsDir, "example", "*", "analysis.md"))
	if len(reports) != 1 {
		t.Fatalf("missing analysis: %v %s", reports, output.String())
	}
	if reports[0] != filepath.Join(a.LLMReportsDir, "example", "1", "analysis.md") {
		t.Fatalf("unexpected report path: %s", reports[0])
	}
	if _, err := os.Stat(filepath.Join(a.LLMReportsDir, "example", "1", "analysis.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Постмортем готов. Отчёт:") {
		t.Fatalf("missing readable LLM result: %s", output.String())
	}
}

func TestAuditLLMFailureFallsBackToAnsibleResult(t *testing.T) {
	for _, failure := range []string{"missing-binary", "client-error", "missing-task"} {
		for _, passed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/passed=%t", failure, passed), func(t *testing.T) {
				a := defaultAuditOptions()
				a.AgentDir, a.ReportsDir, a.LLMConfig = t.TempDir(), t.TempDir(), "local-profile.json"
				a.LLMReportsDir = filepath.Join(t.TempDir(), "llm")
				dir := t.TempDir()
				if failure != "missing-task" {
					if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Task"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "missing-binary" {
					a.Binary = filepath.Join(t.TempDir(), "missing-python.exe")
				}
				if failure == "client-error" {
					a.Binary, _ = os.Executable()
					t.Setenv("INCIDENTCTL_TEST_AGENT_FAIL", "1")
				}
				var warning string
				a.OnWarning = func(message string) { warning = message }
				var want error
				if !passed {
					want = errors.New("Ansible validation failed")
				}
				calls := 0
				var output bytes.Buffer
				err := a.verify(&fakeAuditClient{packet: `{"session":"attempt-1"}`}, "/work", "example", dir, &output, func(io.Writer) error { calls++; return want })
				if err != want {
					t.Fatalf("fallback changed Ansible result: got %v, want %v", err, want)
				}
				if calls != 1 {
					t.Fatalf("Ansible runs: %d, want 1", calls)
				}
				if passed && (!strings.Contains(warning, "LLM не удалась") || !strings.Contains(output.String(), "Использован результат Ansible-проверки")) {
					t.Fatalf("missing fallback warning: %s / %s", warning, output.String())
				}
				if !passed {
					if warning != "" || !strings.Contains(output.String(), "Анализ LLM не запускался") {
						t.Fatalf("LLM ran on failed validation: %s / %s", warning, output.String())
					}
					if _, err := os.Stat(a.LLMReportsDir); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed validation created an LLM report: %v", err)
					}
				}
			})
		}
	}
}

func TestSolutionNumbersAreUniqueAcrossConcurrentRuns(t *testing.T) {
	root := t.TempDir()
	first, err := reserveSolutionDir(root, "task-one")
	if err != nil || filepath.Base(first) != "1" {
		t.Fatalf("first solution: %s %v", first, err)
	}
	if err := os.WriteFile(filepath.Join(first, "analysis.md"), []byte("existing report"), 0600); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			dir, err := reserveSolutionDir(root, "task-one")
			if err != nil {
				t.Error(err)
				return
			}
			results <- dir
		}()
	}
	workers.Wait()
	close(results)
	seen := map[string]bool{first: true}
	for dir := range results {
		if seen[dir] {
			t.Fatalf("duplicate solution number: %s", dir)
		}
		seen[dir] = true
	}
	if len(seen) != 9 {
		t.Fatalf("reserved solutions: %d", len(seen))
	}
	data, err := os.ReadFile(filepath.Join(first, "analysis.md"))
	if err != nil || string(data) != "existing report" {
		t.Fatal("existing report changed")
	}
	other, err := reserveSolutionDir(root, "task-two")
	if err != nil || filepath.Base(other) != "1" {
		t.Fatalf("separate task numbering: %s %v", other, err)
	}
}

func TestAuditAutomaticallyResumesAfterEachValidation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		client := &fakeAuditClient{packet: `{"session":"attempt-1"}`}
		a := auditOptions{ReportsDir: t.TempDir(), LLMConfig: "configured", Binary: "missing-binary", AgentDir: t.TempDir()}
		var want error
		if failed {
			want = errors.New("validation failed")
		}
		a.OnWarning = func(string) {
			if strings.Join(client.operations, ",") != "stop,start" {
				t.Fatal("LLM failure occurred before recording resumed")
			}
		}
		err := a.verify(client, "/work", "example", "missing-task", io.Discard, func(io.Writer) error {
			if strings.Join(client.operations, ",") != "stop" {
				t.Fatal("recording must stay stopped during validation")
			}
			return want
		})
		if err != want || strings.Join(client.operations, ",") != "stop,start" {
			t.Fatalf("resume failed: %v %v", err, client.operations)
		}
	}
}

func TestAuditResumeFailureIsVisibleAndPreservesValidationFailure(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`, startErr: errors.New("SSH unavailable")}
	a := auditOptions{ReportsDir: t.TempDir()}
	want := &playbookRunError{Phase: "execute playbook", Err: &remote.CommandError{ExitCode: 2}}
	err := a.verify(client, "/work", "example", "unused", io.Discard, func(io.Writer) error { return want })
	var resumeErr *recordingResumeError
	if !errors.Is(err, want) || !errors.As(err, &resumeErr) {
		t.Fatalf("lost failure details: %v", err)
	}
	if isRejectedVerification(err) {
		t.Fatal("recording failure must not be hidden by rejected solution status")
	}
}
