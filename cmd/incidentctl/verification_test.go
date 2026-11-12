package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
	"incidentctl/internal/verifier"
)

// fakeVerifyClient is the explicit mock of the SSH boundary.
type fakeVerifyClient struct {
	status    verifier.Status
	attempt   string
	calls     []string
	legacyErr error
}

func (f *fakeVerifyClient) RunShellInputContext(_ context.Context, script string, in io.Reader, args ...string) error {
	f.calls = append(f.calls, "upload")
	_, _ = io.Copy(io.Discard, in)
	if len(args) == 3 {
		f.attempt = args[2]
	}
	return nil
}
func (f *fakeVerifyClient) RunScriptContext(_ context.Context, _ string, _ ...string) error {
	f.calls = append(f.calls, "script")
	if f.status != verifier.StatusPass {
		return &remote.CommandError{ExitCode: 2} // a failed check fails the play
	}
	return nil
}
func (f *fakeVerifyClient) RunScriptOutputContext(_ context.Context, _ string, args ...string) (string, error) {
	f.calls = append(f.calls, "fetch")
	check := verifier.Check{ID: "a", Status: f.status}
	report := verifier.Report{Schema: verifier.SchemaV1, Scenario: "ex", AttemptID: f.attempt, Complete: true, Status: f.status, Checks: []verifier.Check{check}}
	data, _ := json.Marshal(report)
	return string(data), nil
}
func (f *fakeVerifyClient) RunShellInput(string, io.Reader, ...string) error {
	f.calls = append(f.calls, "legacy-upload")
	return f.legacyErr
}
func (f *fakeVerifyClient) RunScript(string, ...string) error {
	f.calls = append(f.calls, "legacy-run")
	return nil
}

func verifyTestExercise(t *testing.T, manifest bool) (config.Exercise, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "ex")
	for _, p := range []string{"verification/playbook.yml", "../collections/ansible_collections/x/y/a.txt"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ex := config.Exercise{Name: "ex", Root: root, Playbooks: config.ExerciseActions{Verification: "verification/playbook.yml"},
		Verification: config.Verification{Playbook: "verification/playbook.yml"}}
	if manifest {
		ex.Manifest = true
		ex.Verification.Timeout = 30 * time.Second
		ex.Verification.Report = &config.ReportContract{Schema: config.ReportSchemaV1, Checks: []string{"a"}}
	}
	return ex, dir
}

func TestVerifyExerciseRoutesManifestToReportEngine(t *testing.T) {
	ex, dir := verifyTestExercise(t, true)
	for status, want := range map[verifier.Status]string{verifier.StatusPass: "", verifier.StatusFail: "rejected", verifier.StatusError: "technical"} {
		client := &fakeVerifyClient{status: status}
		result, err := verifyExercise(context.Background(), client, "/work", ex, dir, nil, io.Discard)
		var rejected *verifier.RejectedError
		var technical *verifier.TechnicalError
		switch want {
		case "":
			if err != nil || result == nil {
				t.Fatalf("pass: %v", err)
			}
		case "rejected":
			if !errors.As(err, &rejected) || !isRejectedVerification(err) || !isExpectedVerificationFailure(err) {
				t.Fatalf("fail: %v", err)
			}
		case "technical":
			if !errors.As(err, &technical) || isRejectedVerification(err) || isExpectedVerificationFailure(err) {
				t.Fatalf("error: %v", err)
			}
		}
		if strings.Contains(strings.Join(client.calls, ","), "legacy") {
			t.Fatal("manifest exercise used the legacy path")
		}
	}
}

func TestVerifyExerciseKeepsLegacyExitCodeContract(t *testing.T) {
	ex, dir := verifyTestExercise(t, false)
	client := &fakeVerifyClient{}
	result, err := verifyExercise(context.Background(), client, "/work", ex, dir, nil, io.Discard)
	if err != nil || result != nil {
		t.Fatalf("%v %v", result, err)
	}
	if got := strings.Join(client.calls, ","); got != "legacy-upload,legacy-run" {
		t.Fatalf("calls = %s", got)
	}
	client = &fakeVerifyClient{legacyErr: &remote.CommandError{ExitCode: 255}}
	_, err = verifyExercise(context.Background(), client, "/work", ex, dir, nil, io.Discard)
	if err == nil || isRejectedVerification(err) {
		t.Fatalf("transport failure must not be a rejection: %v", err)
	}
}

func TestTechnicalErrorsAreNeverRejectedOrExpected(t *testing.T) {
	err := &verifier.TechnicalError{Phase: "fetch report", Err: &remote.CommandError{ExitCode: 2}}
	if isRejectedVerification(err) || isExpectedVerificationFailure(err) {
		t.Fatal("technical error treated as verdict")
	}
	joined := errors.Join(&verifier.RejectedError{Result: &verifier.Result{}}, &recordingResumeError{Err: errors.New("x")})
	if isRejectedVerification(joined) {
		t.Fatal("resume failure must win over rejection")
	}
}

func TestAuditPersistsValidatedReportForPostmortem(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`}
	a := auditOptions{ReportsDir: t.TempDir()}
	raw := []byte(`{"schema":"incidentsim.verify/v1","scenario":"example","attempt_id":"ABCDEFGHIJKLMNOPQRSTUVWXYZ","complete":true,"status":"fail","checks":[{"id":"a","status":"fail"}]}`)
	var report verifier.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	result := &verifier.Result{AttemptID: report.AttemptID, Report: report, Raw: raw}
	err := a.verifyResult(client, "/work", "example", "unused", io.Discard, func(io.Writer) (*verifier.Result, error) {
		if strings.Join(client.operations, ",") != "stop" {
			t.Fatal("verifier ran before audit stop")
		}
		return result, &verifier.RejectedError{Result: result}
	})
	if !isRejectedVerification(err) || strings.Join(client.operations, ",") != "stop,start" {
		t.Fatalf("%v %v", err, client.operations)
	}
	entries, _ := os.ReadDir(a.ReportsDir)
	dir := filepath.Join(a.ReportsDir, entries[0].Name())
	saved, err := os.ReadFile(filepath.Join(dir, "verification-report.json"))
	if err != nil || !bytes.Equal(bytes.TrimSpace(saved), raw) {
		t.Fatalf("saved report: %s %v", saved, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "verification-report.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	validation, _ := os.ReadFile(filepath.Join(dir, "validation.json"))
	var decoded struct {
		Passed bool            `json:"passed"`
		Status string          `json:"status"`
		Report json.RawMessage `json:"report"`
	}
	if err := json.Unmarshal(validation, &decoded); err != nil || decoded.Passed || decoded.Status != "fail" || len(decoded.Report) == 0 {
		t.Fatalf("validation: %s %v", validation, err)
	}
}

func TestAuditResumeFailureAfterPassingReportIsNotARejection(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`, startErr: errors.New("ssh down")}
	a := auditOptions{ReportsDir: t.TempDir()}
	err := a.verifyResult(client, "/work", "example", "unused", io.Discard, func(io.Writer) (*verifier.Result, error) {
		return nil, &verifier.RejectedError{Result: &verifier.Result{}}
	})
	if err == nil || isRejectedVerification(err) {
		t.Fatalf("audit start failure became a rejected solution: %v", err)
	}
}

func TestAuditDoesNotStartLLMWhenRecordingCouldNotResume(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`, startErr: errors.New("ssh down")}
	a := auditOptions{ReportsDir: t.TempDir(), LLMConfig: "configured", Python: "missing-python", AgentDir: t.TempDir()}
	warned := false
	a.OnWarning = func(string) { warned = true } // only reached when analysis was attempted
	var output bytes.Buffer
	err := a.verifyResult(client, "/work", "example", t.TempDir(), &output, func(io.Writer) (*verifier.Result, error) { return nil, nil })
	var resumeErr *recordingResumeError
	if !errors.As(err, &resumeErr) {
		t.Fatalf("resume failure must be reported: %v", err)
	}
	if warned || strings.Contains(output.String(), "LLM") && !strings.Contains(output.String(), "не запускался") {
		t.Fatalf("LLM analysis started although recording was not resumed: %s", output.String())
	}
	entries, _ := os.ReadDir(filepath.Join(a.LLMReportsDir))
	if len(entries) != 0 {
		t.Fatal("a postmortem directory was reserved")
	}
}

func TestAuditTechnicalErrorWritesNoReport(t *testing.T) {
	client := &fakeAuditClient{packet: `{"session":"attempt-1"}`}
	a := auditOptions{ReportsDir: t.TempDir()}
	err := a.verifyResult(client, "/work", "example", "unused", io.Discard, func(io.Writer) (*verifier.Result, error) {
		return nil, &verifier.TechnicalError{Phase: "fetch report", Err: errors.New("none")}
	})
	if err == nil || isRejectedVerification(err) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(a.ReportsDir)
	if _, err := os.Stat(filepath.Join(a.ReportsDir, entries[0].Name(), "verification-report.json")); !os.IsNotExist(err) {
		t.Fatal("no report should exist")
	}
}
