package main

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"incidentctl/internal/auditbinary"
	"incidentctl/internal/config"
	"incidentctl/internal/verifier"
)

type auditOptions struct {
	AgentDir, Binary, LLMConfig, ReportsDir, LLMReportsDir string
	OnWarning                                              func(string)
	LLM                                                    *config.LLMConfig
}

func defaultAuditOptions() auditOptions {
	return auditOptions{ReportsDir: "analysis", LLMReportsDir: "llm-reports"}
}

type auditClient interface {
	RunScriptOutput(string, ...string) (string, error)
	RunShellInput(string, io.Reader, ...string) error
}

type recordingResumeError struct{ Err error }

func (e *recordingResumeError) Error() string {
	return "не удалось автоматически включить запись пользовательских событий: " + e.Err.Error()
}
func (e *recordingResumeError) Unwrap() error { return e.Err }

// Only collector files are uploaded. LLM configuration and credentials stay local.
func auditBundle(root string) ([]byte, error) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, name := range []string{"bin/linux-amd64/incident-audit"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("audit file must be regular: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := w.WriteHeader(&tar.Header{Name: "incident-audit", Mode: 0700, Size: int64(len(data))}); err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (a auditOptions) prepare(client auditClient, work, name string) error {
	var bundle []byte
	var err error
	if a.AgentDir != "" {
		bundle, err = auditBundle(a.AgentDir)
	} else {
		var binary []byte
		binary, err = auditbinary.Read("linux", "amd64")
		if err == nil {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			err = w.WriteHeader(&tar.Header{Name: "incident-audit", Mode: 0700, Size: int64(len(binary))})
			if err == nil {
				_, err = w.Write(binary)
			}
			if closeErr := w.Close(); err == nil {
				err = closeErr
			}
			bundle = b.Bytes()
		}
	}
	if err != nil {
		return fmt.Errorf("bundle audit-agent (-audit-agent-dir): %w", err)
	}
	// The staged Go binary closes an earlier attempt before installation/restart,
	// including when upgrading an existing Python installation.
	if err := client.RunShellInput(auditInstallScript, bytes.NewReader(bundle), work, name); err != nil {
		return fmt.Errorf("install audit-agent: %w", err)
	}
	return nil
}

func (a auditOptions) start(client auditClient, work, name string) error {
	id := rand.Text()
	_, err := client.RunScriptOutput(auditControlScript, work, name, "start", id)
	if err != nil {
		return fmt.Errorf("start audit recording: %w", err)
	}
	return nil
}

// stop is deliberately before the callback: even exercise upload must not enter the recording.
func (a auditOptions) verify(client auditClient, work, name, dir string, output io.Writer, validate func(io.Writer) error) error {
	return a.verifyResult(client, work, name, dir, output, func(out io.Writer) (*verifier.Result, error) { return nil, validate(out) })
}

// verifyResult is verify for validators that return a structured report. The
// validated report is persisted next to the audit packet for the postmortem.
func (a auditOptions) verifyResult(client auditClient, work, name, dir string, output io.Writer, validate func(io.Writer) (*verifier.Result, error)) error {
	packet, err := client.RunScriptOutput(auditControlScript, work, name, "stop", "")
	if err != nil {
		return fmt.Errorf("stop audit recording; verification blocked: %w", err)
	}
	var envelope struct {
		Session string `json:"session"`
	}
	if json.Unmarshal([]byte(packet), &envelope) != nil || envelope.Session == "" {
		return errors.New("invalid audit packet; verification blocked")
	}
	if err := os.MkdirAll(a.ReportsDir, 0700); err != nil {
		return err
	}
	report, err := os.MkdirTemp(a.ReportsDir, name+"-")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(report, "llm.json"), []byte(packet+"\n"), 0600); err != nil {
		return err
	}
	var validation bytes.Buffer
	verifyResult, verifyErr := validate(io.MultiWriter(output, &validation))
	if verifyResult == nil {
		var rejected *verifier.RejectedError
		var technical *verifier.TechnicalError
		if errors.As(verifyErr, &rejected) {
			verifyResult = rejected.Result
		} else if errors.As(verifyErr, &technical) {
			verifyResult = technical.Result
		}
	}
	// Reopen recording immediately after Ansible, before saving reports or waiting
	// for the LLM. Each recording gets a new ID; the sealed packet stays unchanged.
	var resumeErr error
	if err := a.start(client, work, name); err != nil {
		resumeErr = &recordingResumeError{Err: err}
		fmt.Fprintf(output, "\nОшибка записи: %v\n", resumeErr)
	} else {
		fmt.Fprintln(output, "\nЗапись пользовательских событий включена автоматически.")
	}
	result := struct {
		Passed bool            `json:"passed"`
		Error  string          `json:"error,omitempty"`
		Output string          `json:"output"`
		Status string          `json:"status,omitempty"`
		Report json.RawMessage `json:"report,omitempty"`
	}{Passed: verifyErr == nil, Output: validation.String()}
	if verifyErr != nil {
		result.Error = verifyErr.Error()
	}
	var reportErr error
	if verifyResult != nil && len(verifyResult.Raw) > 0 {
		result.Status, result.Report = string(verifyResult.Report.Status), json.RawMessage(verifyResult.Raw)
		reportErr = writeFileAtomic(filepath.Join(report, "verification-report.json"), append(append([]byte(nil), verifyResult.Raw...), '\n'))
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		err = errors.Join(reportErr, writeFileAtomic(filepath.Join(report, "validation.json"), data))
	}
	if err != nil {
		return errors.Join(verifyErr, resumeErr, err)
	}
	fmt.Fprintf(output, "\nЛоги проверенной попытки сохранены: %s\n", report)
	if a.llmEnabled() && verifyErr == nil {
		if err := a.analyzeReport(report, name, dir, output); err != nil {
			warning := "Проверка с помощью LLM не удалась. Использован результат Ansible-проверки."
			fmt.Fprintf(output, "\nПредупреждение: %s\nПричина: %v\nЛоги сохранены: %s\n", warning, err, report)
			if a.OnWarning != nil {
				a.OnWarning(warning)
			}
		}
	} else if a.llmEnabled() {
		fmt.Fprintln(output, "Анализ LLM не запускался: Ansible-проверка ещё не пройдена.")
	}
	if resumeErr != nil {
		return errors.Join(verifyErr, resumeErr)
	}
	return verifyErr
}

// writeFileAtomic writes a private file via a temporary name and rename, so a
// reader never observes a partial report.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (a auditOptions) analyzeReport(report, name, dir string, output io.Writer) error {
	task, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(report, "task.md"), task, 0600); err != nil {
		return err
	}
	llmConfig, env, cleanup, err := a.analysisSettings()
	if err != nil {
		return err
	}
	defer cleanup()
	solution, err := reserveSolutionDir(a.LLMReportsDir, name)
	if err != nil {
		return err
	}
	staged := filepath.Join(solution, ".result")
	binary, binaryCleanup, err := a.analysisExecutable()
	if err != nil {
		return err
	}
	defer binaryCleanup()
	cmd := exec.Command(binary, "analyze", "--config", llmConfig,
		"--input", filepath.Join(report, "llm.json"), "--task", filepath.Join(report, "task.md"),
		"--validation", filepath.Join(report, "validation.json"), "--output", staged)
	var summary bytes.Buffer
	cmd.Env = env
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &summary, io.MultiWriter(output, &diagnostics)
	if err := cmd.Run(); err != nil {
		if diagnostics.Len() > 0 {
			return fmt.Errorf("%w: %s", err, diagnostics.String())
		}
		return err
	}
	for _, filename := range []string{"analysis.md", "analysis.json"} {
		if err := os.Rename(filepath.Join(staged, filename), filepath.Join(solution, filename)); err != nil {
			return err
		}
	}
	if err := os.Remove(staged); err != nil {
		return err
	}
	fmt.Fprintf(output, "\nПостмортем LLM готов. Отчёт: %s\n", filepath.Join(solution, "analysis.md"))
	return nil
}

func (a auditOptions) analysisExecutable() (string, func(), error) {
	if a.Binary != "" {
		return a.Binary, func() {}, nil
	}
	if a.AgentDir != "" {
		name := "incident-audit"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return filepath.Join(a.AgentDir, "bin", runtime.GOOS+"-"+runtime.GOARCH, name), func() {}, nil
	}
	binary, err := auditbinary.Native()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "incidentctl-agent-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	name := "incident-audit"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, binary, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// Mkdir reserves each solution number atomically across CLI processes. Failed
// analysis attempts retain their number so reports can never overwrite a prior run.
func reserveSolutionDir(root, name string) (string, error) {
	if !config.SafeName(name) {
		return "", fmt.Errorf("invalid exercise name %q", name)
	}
	task := filepath.Join(root, name)
	if err := os.MkdirAll(task, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(task)
	if err != nil {
		return "", err
	}
	next := 1
	for _, entry := range entries {
		n, err := strconv.Atoi(entry.Name())
		if err == nil && n >= next {
			next = n + 1
		}
	}
	for {
		dir := filepath.Join(task, strconv.Itoa(next))
		err := os.Mkdir(dir, 0700)
		if errors.Is(err, os.ErrExist) {
			next++
			continue
		}
		return dir, err
	}
}

const auditInstallScript = `set -euo pipefail
stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
tar -xf - -C "$stage" --no-same-owner --no-same-permissions
chmod 700 "$stage/incident-audit"
prefix=()
if [ "$(id -u)" != 0 ]; then prefix=(sudo -n); fi
"${prefix[@]}" "$stage/incident-audit" control "$1" "$2" reset ""
"${prefix[@]}" "$stage/incident-audit" install
`

// Keep the durable attempt pointer in the separate guest agent.
const auditControlScript = `set -euo pipefail
prefix=()
if [ "$(id -u)" != 0 ]; then prefix=(sudo -n); fi
"${prefix[@]}" /usr/local/sbin/incident-audit control "$@"
`
