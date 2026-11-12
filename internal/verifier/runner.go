package verifier

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
)

const (
	maxBundleBytes = 16 << 20
	uploadTimeout  = 2 * time.Minute
	fetchTimeout   = time.Minute
	cleanupTimeout = 20 * time.Second
	executionGrace = 30 * time.Second
)

// Remote is the boundary to the VM. *remote.Client implements it; tests
// replace it with fakes or with a local shell.
type Remote interface {
	RunShellInputContext(ctx context.Context, script string, input io.Reader, args ...string) error
	RunScriptContext(ctx context.Context, script string, args ...string) error
	RunScriptOutputContext(ctx context.Context, script string, args ...string) (string, error)
}

var _ Remote = (*remote.Client)(nil)

// Spec describes one verification run.
type Spec struct {
	Name           string
	ExerciseDir    string
	CollectionsDir string
	WorkDir        string
	// Playbook is relative to ExerciseDir; its directory is shipped.
	Playbook  string
	Timeout   time.Duration
	Required  []string
	ExtraArgs []string
}

// Result is a validated report together with the exact bytes received.
type Result struct {
	AttemptID string
	Report    Report
	Raw       []byte
}

// RejectedError is returned when a validated report contains an explicit
// failed check: the only outcome that proves an unsolved or reproduced incident.
type RejectedError struct{ Result *Result }

func (e *RejectedError) Error() string {
	return "verification rejected: " + failedChecks(e.Result.Report)
}

// TechnicalError is every other non-pass outcome: transport, upload, timeout,
// missing/invalid report, or a report with technical error checks.
type TechnicalError struct {
	Phase string
	Err   error
	// Result is set when a valid report exists (status error).
	Result *Result
}

func (e *TechnicalError) Error() string { return "verification " + e.Phase + ": " + e.Err.Error() }
func (e *TechnicalError) Unwrap() error { return e.Err }

func failedChecks(r Report) string {
	var ids []string
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			ids = append(ids, c.ID)
		}
	}
	return strings.Join(ids, ", ")
}

func technical(phase string, err error) *TechnicalError {
	return &TechnicalError{Phase: phase, Err: err}
}

// NewAttemptID returns an unguessable identifier for one verification attempt.
func NewAttemptID() string { return rand.Text() }

// Run executes the verification playbook for spec and returns its verdict.
// A nil error means the report validated and every check passed.
func Run(ctx context.Context, client Remote, spec Spec, out io.Writer) (*Result, error) {
	return run(ctx, client, spec, NewAttemptID(), out)
}

func run(ctx context.Context, client Remote, spec Spec, attempt string, out io.Writer) (*Result, error) {
	if err := spec.validate(); err != nil {
		return nil, technical("prepare", err)
	}
	if !attemptIDPattern.MatchString(attempt) {
		return nil, technical("prepare", errors.New("invalid attempt id"))
	}
	bundle, err := buildBundle(spec)
	if err != nil {
		return nil, technical("prepare", err)
	}
	upCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	err = client.RunShellInputContext(upCtx, uploadScript, bytes.NewReader(bundle), spec.WorkDir, spec.Name, attempt)
	cancel()
	if err != nil {
		return nil, technical("upload", err)
	}
	defer cleanup(client, spec, attempt)

	args := []string{spec.WorkDir, spec.Name, attempt, filepath.ToSlash(spec.Playbook),
		strconv.Itoa(int(spec.Timeout / time.Second)), config.ReportSchemaV1}
	args = append(args, spec.ExtraArgs...)
	execCtx, cancel := context.WithTimeout(ctx, spec.Timeout+executionGrace)
	execErr := client.RunScriptContext(execCtx, executeScript, args...)
	cancel()
	if execErr != nil && (ctx.Err() != nil || !acceptableExit(execErr)) {
		// Timeouts (124/137), signals, syntax/usage errors, SSH failures and
		// cancellation are technical regardless of any report left behind.
		return nil, technical("execute", execErr)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	raw, err := client.RunScriptOutputContext(fetchCtx, fetchScript, spec.WorkDir, spec.Name, attempt, strconv.Itoa(MaxReportBytes))
	cancel()
	if err != nil {
		if execErr != nil {
			err = fmt.Errorf("%w (verifier: %v)", err, execErr)
		}
		return nil, technical("fetch report", err)
	}
	report, err := DecodeReport([]byte(raw), Expectation{
		Scenario: spec.Name, AttemptID: attempt, Schema: config.ReportSchemaV1, Required: spec.Required,
	})
	if err != nil {
		return nil, technical("validate report", err)
	}
	result := &Result{AttemptID: attempt, Report: report, Raw: []byte(raw)}
	if out != nil {
		fmt.Fprint(out, "\n"+report.Summary())
	}
	switch report.Status {
	case StatusPass:
		if execErr != nil {
			return result, &TechnicalError{Phase: "execute", Err: fmt.Errorf("report passed but ansible-playbook failed: %w", execErr), Result: result}
		}
		return result, nil
	case StatusFail:
		if execErr == nil {
			// A clean exit cannot accompany a failed check (the check task fails the play).
			return result, &TechnicalError{Phase: "execute", Err: errors.New("report says fail but ansible-playbook exited 0"), Result: result}
		}
		return result, &RejectedError{Result: result}
	default:
		return result, &TechnicalError{Phase: "verifier error", Err: errors.New("a check could not be evaluated: " + errorChecks(report)), Result: result}
	}
}

// PhaseError tags a legacy verification failure with the phase it happened in
// ("upload exercise" or "execute playbook").
type PhaseError struct {
	Phase string
	Err   error
}

func (e *PhaseError) Error() string { return e.Phase + ": " + e.Err.Error() }
func (e *PhaseError) Unwrap() error { return e.Err }

// RunLegacy runs a vm.json-only verification playbook with its original exit
// code contract (0 passed, 2 rejected, anything else technical), but like the
// manifest engine it works in a fresh attempt directory: the exercise workspace
// is never deleted or re-extracted. The whole exercise is copied (legacy
// playbooks may use sibling files) and the collection is provided when present.
func RunLegacy(ctx context.Context, client Remote, spec Spec, out io.Writer) error {
	attempt := NewAttemptID()
	if err := spec.validateLegacy(); err != nil {
		return &PhaseError{Phase: "upload exercise", Err: err}
	}
	bundle, err := buildLegacyBundle(spec)
	if err != nil {
		return &PhaseError{Phase: "upload exercise", Err: err}
	}
	upCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	err = client.RunShellInputContext(upCtx, uploadScript, bytes.NewReader(bundle), spec.WorkDir, spec.Name, attempt)
	cancel()
	if err != nil {
		return &PhaseError{Phase: "upload exercise", Err: err}
	}
	defer cleanup(client, spec, attempt)
	args := []string{spec.WorkDir, spec.Name, attempt, filepath.ToSlash(spec.Playbook), strconv.Itoa(int(spec.Timeout / time.Second))}
	args = append(args, spec.ExtraArgs...)
	execCtx, cancel := context.WithTimeout(ctx, spec.Timeout+executionGrace)
	defer cancel()
	if err := client.RunScriptContext(execCtx, legacyExecuteScript, args...); err != nil {
		return &PhaseError{Phase: "execute playbook", Err: err}
	}
	return nil
}

func (s Spec) validateLegacy() error {
	if !config.SafeName(s.Name) {
		return fmt.Errorf("invalid exercise name %q", s.Name)
	}
	if !path.IsAbs(s.WorkDir) || path.Clean(s.WorkDir) == "/" || strings.ContainsAny(s.WorkDir, "\r\n") {
		return errors.New("work directory must be an absolute path other than /")
	}
	clean := filepath.Clean(s.Playbook)
	if s.Playbook == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(filepath.ToSlash(clean), "../") {
		return fmt.Errorf("verification playbook %q must be a relative path inside the exercise", s.Playbook)
	}
	if s.Timeout < time.Second || s.ExerciseDir == "" {
		return errors.New("a positive timeout and the exercise directory are required")
	}
	return nil
}

// ansibleTaskFailed is ansible-playbook's status for "a task failed", the only
// non-zero status that may accompany a report.
const ansibleTaskFailed = 2

func acceptableExit(err error) bool {
	var commandErr *remote.CommandError
	return errors.As(err, &commandErr) && commandErr.ExitCode == ansibleTaskFailed
}

func errorChecks(r Report) string {
	var ids []string
	for _, c := range r.Checks {
		if c.Status == StatusError {
			ids = append(ids, c.ID)
		}
	}
	return strings.Join(ids, ", ")
}

func cleanup(client Remote, spec Spec, attempt string) {
	// Best effort: the attempt directory is unique and may be left behind.
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = client.RunScriptContext(ctx, cleanupScript, spec.WorkDir, spec.Name, attempt)
}

func (s Spec) validate() error {
	if !config.SafeName(s.Name) {
		return fmt.Errorf("invalid exercise name %q", s.Name)
	}
	if !path.IsAbs(s.WorkDir) || path.Clean(s.WorkDir) == "/" || strings.ContainsAny(s.WorkDir, "\r\n") {
		return errors.New("work directory must be an absolute path other than /")
	}
	clean := filepath.Clean(s.Playbook)
	if s.Playbook == "" || filepath.IsAbs(clean) || strings.HasPrefix(filepath.ToSlash(clean), "../") || clean == ".." || filepath.Dir(clean) == "." {
		return fmt.Errorf("verification playbook %q must be a file inside a subdirectory of the exercise", s.Playbook)
	}
	if s.Timeout < time.Second {
		return errors.New("verification timeout must be positive")
	}
	if s.ExerciseDir == "" || s.CollectionsDir == "" {
		return errors.New("exercise and collections directories are required")
	}
	return nil
}

// buildBundle ships only the verification directory and the collection. The
// rest of the exercise (and any workspace state on the VM) is never touched.
func buildBundle(spec Spec) ([]byte, error) { return buildBundleMode(spec, false) }

// buildLegacyBundle ships the whole exercise (never into the workspace).
func buildLegacyBundle(spec Spec) ([]byte, error) { return buildBundleMode(spec, true) }

func buildBundleMode(spec Spec, legacy bool) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	total := int64(0)
	add := func(srcRoot, destRoot string, skipTests bool) error {
		info, err := os.Lstat(srcRoot)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", srcRoot)
		}
		return filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(srcRoot, p)
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() && skipTests && (name == "tests" || name == "__pycache__" || name == ".pytest_cache") {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return fmt.Errorf("unsupported file in verification bundle: %s", p)
			}
			header := &tar.Header{Name: path.Join(destRoot, filepath.ToSlash(rel)), ModTime: time.Unix(0, 0)}
			if rel == "." {
				header.Name = destRoot
			}
			if info.IsDir() {
				header.Typeflag, header.Mode = tar.TypeDir, 0o700
				return w.WriteHeader(header)
			}
			total += info.Size()
			if total > maxBundleBytes {
				return fmt.Errorf("verification bundle exceeds %d bytes", maxBundleBytes)
			}
			header.Typeflag, header.Mode, header.Size = tar.TypeReg, 0o600, info.Size()
			if err := w.WriteHeader(header); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		})
	}
	roots := []string{"bundle", "collections"}
	if legacy {
		roots = []string{"collections"} // "bundle" is written by the exercise copy itself
	}
	for _, dir := range roots {
		if err := w.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
			return nil, err
		}
	}
	if legacy {
		if err := add(spec.ExerciseDir, "bundle", false); err != nil {
			return nil, fmt.Errorf("bundle exercise: %w", err)
		}
	} else {
		verificationDir := filepath.Dir(filepath.Clean(spec.Playbook))
		if err := add(filepath.Join(spec.ExerciseDir, verificationDir), path.Join("bundle", filepath.ToSlash(verificationDir)), false); err != nil {
			return nil, fmt.Errorf("bundle verification: %w", err)
		}
	}
	collections := filepath.Join(spec.CollectionsDir, "ansible_collections")
	if _, statErr := os.Stat(collections); legacy && (spec.CollectionsDir == "" || os.IsNotExist(statErr)) {
		// the collection is optional for legacy exercises
	} else if err := add(collections, "collections/ansible_collections", true); err != nil {
		return nil, fmt.Errorf("bundle collection: %w", err)
	}
	if _, err := os.Stat(filepath.Join(spec.ExerciseDir, spec.Playbook)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// The attempt directory is created with plain mkdir (no -p) so it cannot
// pre-exist: a stale report from an earlier attempt can never be picked up.
// Nothing outside the new directory is modified or removed.
const uploadScript = `set -euo pipefail
work="$1"; name="$2"; attempt="$3"
base="$work/.verify/$name"
umask 077
mkdir -p "$base"
mkdir "$base/$attempt"
tar -xf - -C "$base/$attempt" --no-same-owner --no-same-permissions
`

// ansible-playbook's exit status is advisory; the runner trusts the report.
// The script propagates it so the runner can notice a passing report produced
// by a failing run.
const executeScript = `set -uo pipefail
work="$1"; name="$2"; attempt="$3"; playbook="$4"; timeout_seconds="$5"; schema="$6"; shift 6
target="$work/.verify/$name/$attempt"
if [ ! -f "$target/bundle/$playbook" ]; then echo "verification playbook is missing from the bundle" >&2; exit 64; fi
cd "$target/bundle"
export ANSIBLE_NOCOWS=1 ANSIBLE_HOST_KEY_CHECKING=True ANSIBLE_RETRY_FILES_ENABLED=False
export ANSIBLE_COLLECTIONS_PATH="$target/collections"
export ANSIBLE_CALLBACKS_ENABLED=incidentsim.verify.report
export INCIDENTSIM_SCENARIO="$name" INCIDENTSIM_ATTEMPT_ID="$attempt" INCIDENTSIM_REPORT_SCHEMA="$schema"
export INCIDENTSIM_REPORT_PATH="$target/report.json"
timeout --kill-after=10s "$timeout_seconds" ansible-playbook --inventory 'localhost,' --connection local \
  --extra-vars "incidentsim_scenario=$name incidentsim_attempt_id=$attempt" "$playbook" "$@"
rc=$?
if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then echo "verification timed out after ${timeout_seconds}s" >&2; fi
exit "$rc"
`

// Legacy contract: ansible-playbook's own exit status is the verdict (the
// script propagates it, including 124 from a timeout). The collection is put on
// the collections path only when it was shipped.
const legacyExecuteScript = `set -uo pipefail
work="$1"; name="$2"; attempt="$3"; playbook="$4"; timeout_seconds="$5"; shift 5
target="$work/.verify/$name/$attempt"
if [ ! -f "$target/bundle/$playbook" ]; then echo "verification playbook is missing from the bundle" >&2; exit 64; fi
cd "$target/bundle"
export ANSIBLE_NOCOWS=1 ANSIBLE_HOST_KEY_CHECKING=True ANSIBLE_RETRY_FILES_ENABLED=False
if [ -d "$target/collections/ansible_collections" ]; then export ANSIBLE_COLLECTIONS_PATH="$target/collections"; fi
timeout --kill-after=10s "$timeout_seconds" ansible-playbook --inventory 'localhost,' --connection local "$playbook" "$@"
`

const fetchScript = `set -euo pipefail
file="$1/.verify/$2/$3/report.json"; max="$4"
if [ ! -f "$file" ]; then echo "verifier produced no report" >&2; exit 66; fi
size="$(stat -c %s "$file")"
if [ "$size" -gt "$max" ]; then echo "report exceeds $max bytes" >&2; exit 67; fi
cat -- "$file"
`

const cleanupScript = `set -euo pipefail
work="$1"; name="$2"; attempt="$3"
rm -rf -- "$work/.verify/$name/$attempt"
`
