package verifier

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"incidentctl/internal/remote"
)

// localRemote is the explicit mock of the remote boundary: it runs the real
// upload/execute/fetch scripts with a local bash instead of over SSH. There is
// no VM or SSH involved.
type localRemote struct {
	env     []string
	scripts []string
	failOn  string
	failErr error
}

func (l *localRemote) run(ctx context.Context, stdin io.Reader, out io.Writer, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, out, io.Discard
	cmd.Env = append(os.Environ(), l.env...)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return &remote.CommandError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

func (l *localRemote) RunShellInputContext(ctx context.Context, script string, input io.Reader, args ...string) error {
	l.scripts = append(l.scripts, "upload")
	if l.failOn == "upload" {
		return l.failErr
	}
	return l.run(ctx, input, io.Discard, append([]string{"bash", "-c", script, "--"}, args...)...)
}

func (l *localRemote) RunScriptContext(ctx context.Context, script string, args ...string) error {
	name := "execute"
	if strings.Contains(script, "rm -rf") {
		name = "cleanup"
	}
	l.scripts = append(l.scripts, name)
	if l.failOn == name {
		return l.failErr
	}
	return l.run(ctx, strings.NewReader(script), io.Discard, append([]string{"bash", "-s", "--"}, args...)...)
}

func (l *localRemote) RunScriptOutputContext(ctx context.Context, script string, args ...string) (string, error) {
	l.scripts = append(l.scripts, "fetch")
	if l.failOn == "fetch" {
		return "", l.failErr
	}
	var out bytes.Buffer
	err := l.run(ctx, strings.NewReader(script), &out, append([]string{"bash", "-s", "--"}, args...)...)
	return strings.TrimSpace(out.String()), err
}

// fakeAnsible writes a fake ansible-playbook that emits whatever report the
// FAKE_MODE variable asks for, using the explicit INCIDENTSIM_* inputs.
func fakeAnsible(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
echo "collections=$ANSIBLE_COLLECTIONS_PATH callbacks=$ANSIBLE_CALLBACKS_ENABLED cwd=$PWD args=$*" > "$(dirname "$INCIDENTSIM_REPORT_PATH")/invocation.txt"
head() { printf '{"schema":"%s","scenario":"%s","attempt_id":"%s","complete":true,' "$INCIDENTSIM_REPORT_SCHEMA" "$INCIDENTSIM_SCENARIO" "$INCIDENTSIM_ATTEMPT_ID"; }
case "$FAKE_MODE" in
  pass) { head; echo '"status":"pass","checks":[{"id":"a","status":"pass"},{"id":"b","status":"pass"}]}'; } > "$INCIDENTSIM_REPORT_PATH" ;;
  fail) { head; echo '"status":"fail","checks":[{"id":"a","status":"fail","observed":"404","expected":"200"}]}'; } > "$INCIDENTSIM_REPORT_PATH"; exit 2 ;;
  error) { head; echo '"status":"error","checks":[{"id":"a","status":"error","evidence":"psql: not found"}]}'; } > "$INCIDENTSIM_REPORT_PATH"; exit 2 ;;
  pass_but_crash) { head; echo '"status":"pass","checks":[{"id":"a","status":"pass"},{"id":"b","status":"pass"}]}'; } > "$INCIDENTSIM_REPORT_PATH"; exit 4 ;;
  none) exit 2 ;;
  wrong_attempt) printf '{"schema":"%s","scenario":"%s","attempt_id":"ZZZZZZZZZZZZZZZZZZZZZZZZZZ","complete":true,"status":"pass","checks":[{"id":"a","status":"pass"},{"id":"b","status":"pass"}]}' "$INCIDENTSIM_REPORT_SCHEMA" "$INCIDENTSIM_SCENARIO" > "$INCIDENTSIM_REPORT_PATH" ;;
  garbage) echo 'not json' > "$INCIDENTSIM_REPORT_PATH" ;;
  empty) : > "$INCIDENTSIM_REPORT_PATH" ;;
  huge) command head -c 2000000 /dev/zero | tr '\0' ' ' > "$INCIDENTSIM_REPORT_PATH" ;;
  sleep) sleep 60 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ansible-playbook"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

type fixture struct {
	spec   Spec
	remote *localRemote
	work   string
}

func newFixture(t *testing.T, mode string) *fixture {
	t.Helper()
	root := t.TempDir()
	exercise := filepath.Join(root, "nginx-wrong-api")
	collections := filepath.Join(root, "collections")
	work := filepath.Join(root, "work")
	for path, content := range map[string]string{
		filepath.Join(exercise, "verification", "playbook.yml"):                                  "---\n",
		filepath.Join(exercise, "verification", "extra", "tasks.yml"):                            "---\n",
		filepath.Join(exercise, "infra", "playbook.yml"):                                         "infra must not ship\n",
		filepath.Join(collections, "ansible_collections", "incidentsim", "verify", "x.py"):       "x\n",
		filepath.Join(collections, "ansible_collections", "incidentsim", "verify", "tests", "t"): "tests must not ship\n",
		filepath.Join(work, "nginx-wrong-api", "state.txt"):                                      "workspace state\n",
		filepath.Join(work, ".verify", "nginx-wrong-api", "OLD", "report.json"):                  "stale\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{
		work: work,
		spec: Spec{Name: "nginx-wrong-api", ExerciseDir: exercise, CollectionsDir: collections, WorkDir: work,
			Playbook: "verification/playbook.yml", Timeout: 5 * time.Second, Required: []string{"a", "b"}},
		remote: &localRemote{env: []string{"PATH=" + fakeAnsible(t) + ":" + os.Getenv("PATH"), "FAKE_MODE=" + mode}},
	}
}

const fixedAttempt = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func (f *fixture) attemptDir() string {
	return filepath.Join(f.work, ".verify", f.spec.Name, fixedAttempt)
}

func TestRunPass(t *testing.T) {
	f := newFixture(t, "pass")
	var out bytes.Buffer
	result, err := run(context.Background(), f.remote, f.spec, fixedAttempt, &out)
	if err != nil || result == nil || result.Report.Status != StatusPass || result.AttemptID != fixedAttempt {
		t.Fatalf("%+v %v", result, err)
	}
	if !bytes.Contains(result.Raw, []byte(`"attempt_id":"`+fixedAttempt+`"`)) || !strings.Contains(out.String(), "PASS") {
		t.Fatalf("raw=%s out=%s", result.Raw, out.String())
	}
	if got := strings.Join(f.remote.scripts, ","); got != "upload,execute,fetch,cleanup" {
		t.Fatalf("order = %s", got)
	}
}

func TestRunWiresCollectionAndExplicitInputs(t *testing.T) {
	f := newFixture(t, "pass")
	f.spec.ExtraArgs = []string{"-e", "extra=1"}
	// Keep the attempt directory to inspect it: make cleanup fail silently.
	f.remote.failOn, f.remote.failErr = "cleanup", errors.New("skip")
	if _, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.attemptDir(), "invocation.txt"))
	if err != nil {
		t.Fatal(err)
	}
	invocation := string(data)
	for _, want := range []string{
		"collections=" + filepath.Join(f.attemptDir(), "collections"),
		"callbacks=incidentsim.verify.report",
		"cwd=" + filepath.Join(f.attemptDir(), "bundle"),
		"incidentsim_scenario=nginx-wrong-api", "incidentsim_attempt_id=" + fixedAttempt,
		"verification/playbook.yml", "extra=1",
	} {
		if !strings.Contains(invocation, want) {
			t.Fatalf("missing %q in %s", want, invocation)
		}
	}
	for path, shouldExist := range map[string]bool{
		"bundle/verification/playbook.yml":                           true,
		"bundle/verification/extra/tasks.yml":                        true,
		"bundle/infra/playbook.yml":                                  false,
		"collections/ansible_collections/incidentsim/verify/x.py":    true,
		"collections/ansible_collections/incidentsim/verify/tests/t": false,
	} {
		_, err := os.Stat(filepath.Join(f.attemptDir(), path))
		if (err == nil) != shouldExist {
			t.Fatalf("%s exists=%v want %v", path, err == nil, shouldExist)
		}
	}
}

func TestRunNeverTouchesWorkspaceOrOldAttempts(t *testing.T) {
	f := newFixture(t, "pass")
	if _, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"nginx-wrong-api/state.txt", ".verify/nginx-wrong-api/OLD/report.json"} {
		if _, err := os.Stat(filepath.Join(f.work, p)); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
	if _, err := os.Stat(f.attemptDir()); !os.IsNotExist(err) {
		t.Fatal("attempt directory should be cleaned up after success")
	}
}

func TestRunAttemptDirectoryIsUnique(t *testing.T) {
	f := newFixture(t, "pass")
	f.remote.failOn, f.remote.failErr = "cleanup", errors.New("skip")
	if _, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil); err != nil {
		t.Fatal(err)
	}
	// Reusing an attempt ID (and thus a stale report) must be refused.
	f.remote.env[1] = "FAKE_MODE=none"
	f.remote.failOn = ""
	_, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil)
	var te *TechnicalError
	if !errors.As(err, &te) || te.Phase != "upload" {
		t.Fatalf("want upload technical error, got %v", err)
	}
}

func TestRunVerdicts(t *testing.T) {
	cases := []struct {
		mode      string
		rejected  bool
		technical string // phase, empty when no technical error expected
		hasResult bool
	}{
		{"fail", true, "", true},
		{"error", false, "verifier error", true},
		{"pass_but_crash", false, "execute", true},
		{"none", false, "fetch report", false},
		{"wrong_attempt", false, "validate report", false},
		{"garbage", false, "validate report", false},
		{"empty", false, "validate report", false},
		{"huge", false, "fetch report", false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t, tc.mode)
			result, err := run(context.Background(), f.remote, f.spec, fixedAttempt, io.Discard)
			var rejected *RejectedError
			var te *TechnicalError
			if tc.rejected {
				if !errors.As(err, &rejected) || errors.As(err, &te) {
					t.Fatalf("want RejectedError, got %v", err)
				}
			} else {
				if errors.As(err, &rejected) || !errors.As(err, &te) || te.Phase != tc.technical {
					t.Fatalf("want technical %q, got %v", tc.technical, err)
				}
			}
			if (result != nil) != tc.hasResult {
				t.Fatalf("result presence = %v", result != nil)
			}
		})
	}
}

func TestRunTransportAndRemoteFailures(t *testing.T) {
	for _, stage := range []string{"upload", "execute", "fetch"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t, "pass")
			f.remote.failOn, f.remote.failErr = stage, &remote.CommandError{ExitCode: 255}
			_, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil)
			var te *TechnicalError
			var rejected *RejectedError
			if !errors.As(err, &te) || errors.As(err, &rejected) || !remote.IsTransportError(err) {
				t.Fatalf("SSH failure must be a technical error, got %v", err)
			}
		})
	}
}

func TestRunTimeoutIsTechnical(t *testing.T) {
	f := newFixture(t, "sleep")
	f.spec.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run(ctx, f.remote, f.spec, fixedAttempt, nil)
	var te *TechnicalError
	if !errors.As(err, &te) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want technical deadline error, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("verification did not stop on context cancellation")
	}
}

func TestRunRemoteTimeoutProducesNoVerdict(t *testing.T) {
	f := newFixture(t, "sleep")
	f.spec.Timeout = time.Second
	_, err := run(context.Background(), f.remote, f.spec, fixedAttempt, nil)
	var te *TechnicalError
	var rejected *RejectedError
	if !errors.As(err, &te) || errors.As(err, &rejected) || te.Phase != "fetch report" {
		t.Fatalf("timeout must not be a rejected solution: %v", err)
	}
}

func TestSpecValidation(t *testing.T) {
	good := newFixture(t, "pass").spec
	cases := map[string]func(*Spec){
		"name":      func(s *Spec) { s.Name = "../x" },
		"workdir":   func(s *Spec) { s.WorkDir = "relative" },
		"root work": func(s *Spec) { s.WorkDir = "/" },
		"playbook":  func(s *Spec) { s.Playbook = "../playbook.yml" },
		"top level": func(s *Spec) { s.Playbook = "playbook.yml" },
		"timeout":   func(s *Spec) { s.Timeout = 0 },
	}
	for name, mutate := range cases {
		spec := good
		mutate(&spec)
		if err := spec.validate(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRejectsSymlinks(t *testing.T) {
	f := newFixture(t, "pass")
	if err := os.Symlink("/etc/passwd", filepath.Join(f.spec.ExerciseDir, "verification", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := buildBundle(f.spec); err == nil {
		t.Fatal("symlink must be rejected")
	}
}

func TestAttemptIDsAreUniqueAndWellFormed(t *testing.T) {
	a, b := NewAttemptID(), NewAttemptID()
	if a == b || !attemptIDPattern.MatchString(a) {
		t.Fatalf("%q %q", a, b)
	}
}
