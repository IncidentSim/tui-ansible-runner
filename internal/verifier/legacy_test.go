package verifier

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"incidentctl/internal/remote"
)

// fakeLegacyAnsible exits with FAKE_RC and records how it was invoked.
func fakeLegacyAnsible(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
echo "cwd=$PWD collections=${ANSIBLE_COLLECTIONS_PATH:-} callbacks=${ANSIBLE_CALLBACKS_ENABLED:-} args=$*" > "$(dirname "$PWD")/invocation.txt"
if [ -n "${FAKE_SLEEP:-}" ]; then sleep "$FAKE_SLEEP"; fi
exit "${FAKE_RC:-0}"
`
	if err := os.WriteFile(filepath.Join(dir, "ansible-playbook"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newLegacyFixture(t *testing.T, rc string, withCollection bool) (*fixture, string) {
	t.Helper()
	f := newFixture(t, "pass")
	if !withCollection {
		f.spec.CollectionsDir = ""
	}
	f.spec.Required = nil
	f.remote.env = []string{"PATH=" + fakeLegacyAnsible(t) + ":" + os.Getenv("PATH"), "FAKE_RC=" + rc}
	sentinel := filepath.Join(f.work, f.spec.Name, "state.txt") // created by newFixture
	return f, sentinel
}

func TestLegacyVerificationNeverResetsTheWorkspace(t *testing.T) {
	f, sentinel := newLegacyFixture(t, "0", false)
	extra := filepath.Join(f.work, f.spec.Name, "provisioned", "data.db")
	if err := os.MkdirAll(filepath.Dir(extra), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte("live state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RunLegacy(context.Background(), f.remote, f.spec, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sentinel, extra, filepath.Join(f.work, ".verify", f.spec.Name, "OLD", "report.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was destroyed: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.work, f.spec.Name, "verification")); !os.IsNotExist(err) {
		t.Fatal("exercise files were extracted into the workspace")
	}
	if got := strings.Join(f.remote.scripts, ","); got != "upload,execute,cleanup" {
		t.Fatalf("order = %s", got)
	}
}

func TestLegacyExitCodeContractTimeoutAndExtraArgs(t *testing.T) {
	for _, tc := range []struct {
		rc       string
		wantExit int // 0 = nil error
	}{{"0", 0}, {"2", 2}, {"1", 1}, {"4", 4}, {"124", 124}, {"255", 255}} {
		t.Run("rc"+tc.rc, func(t *testing.T) {
			f, _ := newLegacyFixture(t, tc.rc, false)
			f.spec.ExtraArgs = []string{"-e", "x=1"}
			err := RunLegacy(context.Background(), f.remote, f.spec, nil)
			var cmdErr *remote.CommandError
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var phase *PhaseError
			if !errors.As(err, &phase) || phase.Phase != "execute playbook" || !errors.As(err, &cmdErr) || cmdErr.ExitCode != tc.wantExit {
				t.Fatalf("want execute playbook exit %d, got %v", tc.wantExit, err)
			}
		})
	}
}

func TestLegacyRunsFromAnIsolatedFullCopyWithOptionalCollection(t *testing.T) {
	f, _ := newLegacyFixture(t, "0", true)
	f.remote.failOn, f.remote.failErr = "cleanup", errors.New("keep")
	if err := RunLegacy(context.Background(), f.remote, f.spec, nil); err != nil {
		t.Fatal(err)
	}
	attempts, _ := filepath.Glob(filepath.Join(f.work, ".verify", f.spec.Name, "*", "invocation.txt"))
	if len(attempts) != 1 {
		t.Fatalf("attempts: %v", attempts)
	}
	data, _ := os.ReadFile(attempts[0])
	invocation := string(data)
	root := filepath.Dir(attempts[0])
	for _, want := range []string{"cwd=" + filepath.Join(root, "bundle"), "collections=" + filepath.Join(root, "collections"), "verification/playbook.yml", "--connection local"} {
		if !strings.Contains(invocation, want) {
			t.Fatalf("missing %q in %s", want, invocation)
		}
	}
	if strings.Contains(invocation, "incidentsim.verify.report") {
		t.Fatal("legacy runs must not depend on the report callback")
	}
	// whole exercise is copied (legacy playbooks may use sibling files), still outside the workspace
	if _, err := os.Stat(filepath.Join(root, "bundle", "infra", "playbook.yml")); err != nil {
		t.Fatal("legacy bundle should contain the exercise files", err)
	}
}

func TestLegacyTimeoutAndCancellationAreBounded(t *testing.T) {
	f, _ := newLegacyFixture(t, "0", false)
	f.remote.env = append(f.remote.env, "FAKE_SLEEP=60")
	f.spec.Timeout = time.Second
	start := time.Now()
	err := RunLegacy(context.Background(), f.remote, f.spec, nil)
	var cmdErr *remote.CommandError
	if !errors.As(err, &cmdErr) || cmdErr.ExitCode != 124 || time.Since(start) > 20*time.Second {
		t.Fatalf("remote timeout must stop the playbook with exit 124: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunLegacy(ctx, f.remote, f.spec, nil); err == nil {
		t.Fatal("cancelled legacy run must fail")
	}
}

func TestLegacyUploadFailureIsAnUploadPhase(t *testing.T) {
	f, _ := newLegacyFixture(t, "0", false)
	f.remote.failOn, f.remote.failErr = "upload", &remote.CommandError{ExitCode: 255}
	err := RunLegacy(context.Background(), f.remote, f.spec, &bytes.Buffer{})
	var phase *PhaseError
	if !errors.As(err, &phase) || phase.Phase != "upload exercise" || !remote.IsTransportError(err) {
		t.Fatalf("%v", err)
	}
}
