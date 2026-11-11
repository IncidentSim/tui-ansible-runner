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

	"incidentctl/internal/auditrepo"
	"incidentctl/internal/config"
)

type auditOptions struct {
	AgentDir, Python, LLMConfig, ReportsDir, LLMReportsDir string
	OnWarning                                              func(string)
	Source                                                 *auditrepo.Source
	LLM                                                    *config.LLMConfig
}

func defaultAuditOptions() auditOptions {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	return auditOptions{Python: python, ReportsDir: "analysis", LLMReportsDir: "llm-reports"}
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
	for _, name := range []string{"install.py", "src/incident-audit.py", "src/incident_audit_capture.py", "src/incident_audit_compact.py", "config/50-incidentsim.rules", "config/incidentsim-json.conf", "config/incidentsim-audit.sh"} {
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
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
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
	agentDir, err := a.agentDirectory()
	if err != nil {
		return err
	}
	bundle, err := auditBundle(agentDir)
	if err != nil {
		return fmt.Errorf("bundle audit-agent (-audit-agent-dir): %w", err)
	}
	// Close an earlier attempt before any provisioning or collector restart.
	if _, err := client.RunScriptOutput(auditControlScript, work, name, "reset", ""); err != nil {
		return fmt.Errorf("close previous audit attempt: %w", err)
	}
	if err := client.RunShellInput(auditInstallScript, bytes.NewReader(bundle)); err != nil {
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
	verifyErr := validate(io.MultiWriter(output, &validation))
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
		Passed bool   `json:"passed"`
		Error  string `json:"error,omitempty"`
		Output string `json:"output"`
	}{Passed: verifyErr == nil, Output: validation.String()}
	if verifyErr != nil {
		result.Error = verifyErr.Error()
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(report, "validation.json"), data, 0600)
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

func (a auditOptions) analyzeReport(report, name, dir string, output io.Writer) error {
	task, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(report, "task.md"), task, 0600); err != nil {
		return err
	}
	agentDir, err := a.agentDirectory()
	if err != nil {
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
	cmd := exec.Command(a.Python, filepath.Join(agentDir, "src", "analyze.py"), "--config", llmConfig,
		"--input", filepath.Join(report, "llm.json"), "--task", filepath.Join(report, "task.md"),
		"--validation", filepath.Join(report, "validation.json"), "--output", staged)
	var summary bytes.Buffer
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = &summary, output
	if err := cmd.Run(); err != nil {
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
if [ "$(id -u)" = 0 ]; then
  python3 "$stage/install.py"
else
  sudo -n python3 "$stage/install.py"
fi
`

// Persist the attempt on the VM so separate CLI processes and retries use the same ID.
// The pointer identifies the current attempt across exercises on this VM.
const auditControlScript = `set -euo pipefail
prefix=()
if [ "$(id -u)" != 0 ]; then prefix=(sudo -n); fi
"${prefix[@]}" python3 - "$@" <<'PY'
import fcntl, json, os, subprocess, sys
from pathlib import Path
work, name, action, session = sys.argv[1:]
os.umask(0o077)
root = Path(work) / '.audit'
root.mkdir(parents=True, exist_ok=True)
lock = (root / 'control.lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX)
state_path = root / 'attempt.json'
state = json.loads(state_path.read_text()) if state_path.exists() else None
def call(operation, sid):
    return json.loads(subprocess.check_output(['/usr/local/sbin/incident-audit', operation, '--session-id', sid], text=True))
def save(value):
    temporary = root / 'attempt.tmp'
    temporary.write_text(json.dumps(value))
    temporary.replace(state_path)
def stop_previous():
    if state:
        # Recover a start whose SSH response was lost. start is idempotent for an active ID.
        if state.get('starting'):
            call('start', state['session_id'])
            state.pop('starting', None)
            save(state)
        call('stop', state['session_id'])
if action == 'reset':
    stop_previous()
elif action == 'start':
    # Save before invoking start, allowing recovery when the SSH reply is lost.
    stop_previous()
    state = {'exercise': name, 'session_id': session, 'starting': True}
    save(state)
    result = call('start', session)
    if result['phase'] != 'recording':
        raise RuntimeError('audit did not enter recording phase')
    state.pop('starting')
    save(state)
elif action == 'stop':
    if not state or state['exercise'] != name:
        raise RuntimeError('no audit attempt for this exercise; run infra/start first')
    stop_previous()
    result = call('stop', state['session_id'])
    if result['phase'] != 'stopped':
        raise RuntimeError('audit did not stop')
    packet = Path(result['artifacts']['llm']).read_text()
    if json.loads(packet).get('session') != state['session_id']:
        raise RuntimeError('audit packet belongs to a different attempt')
    print(packet)
else:
    raise RuntimeError('unknown audit action')
PY
`
