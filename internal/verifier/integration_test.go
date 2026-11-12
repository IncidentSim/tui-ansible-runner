package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"incidentctl/internal/config"
)

// Real ansible-playbook, the real exercise repository and the real collection,
// against an isolated local HTTP fixture; only the SSH hop is replaced by a
// local shell. Enable with:
//
//	INCIDENTSIM_EXERCISES=/path/to/exersice INCIDENTSIM_ANSIBLE_BIN=/path/to/venv/bin go test ./internal/verifier
func integrationEnv(t *testing.T) (exercises, ansibleBin string) {
	t.Helper()
	// Defaults: the exersice submodule of this repository and the sibling
	// test-venv created for this integration.
	exercises, ansibleBin = os.Getenv("INCIDENTSIM_EXERCISES"), os.Getenv("INCIDENTSIM_ANSIBLE_BIN")
	if exercises == "" {
		exercises = filepath.Join("..", "..", "exersice")
	}
	if ansibleBin == "" {
		ansibleBin = filepath.Join("..", "..", "..", "test-venv", "bin")
	}
	exercises, _ = filepath.Abs(exercises)
	ansibleBin, _ = filepath.Abs(ansibleBin)
	if _, err := os.Stat(filepath.Join(exercises, "nginx-wrong-api", "manifest.yaml")); err != nil {
		t.Skip("exercise repository with manifests not available (set INCIDENTSIM_EXERCISES)")
	}
	if _, err := os.Stat(filepath.Join(ansibleBin, "ansible-playbook")); err != nil {
		t.Skip("ansible-playbook not found in INCIDENTSIM_ANSIBLE_BIN")
	}
	return exercises, ansibleBin
}

func TestRealAnsibleNginxVerificationEndToEnd(t *testing.T) {
	exercises, ansibleBin := integrationEnv(t)
	exercise, dir, err := config.LoadExercise(exercises, "nginx-wrong-api")
	if err != nil {
		t.Fatal(err)
	}
	if exercise.Verification.Report == nil {
		t.Fatal("nginx-wrong-api must be manifest driven")
	}
	preserve := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := "backend_upstream"
		if preserve {
			host = r.Host
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"host": host})
	}))
	defer server.Close()

	spec := Spec{
		Name: exercise.Name, ExerciseDir: dir, CollectionsDir: filepath.Join(exercise.Root, "collections"),
		WorkDir: filepath.Join(t.TempDir(), "work"), Playbook: exercise.Verification.Playbook,
		Timeout: time.Minute, Required: exercise.Verification.Report.Checks,
		ExtraArgs: []string{"-e", `{"verification_url":"` + server.URL + `/","ansible_python_interpreter":"` + filepath.Join(ansibleBin, "python") + `"}`},
	}
	local := &localRemote{env: []string{"PATH=" + ansibleBin + ":" + os.Getenv("PATH"), "ANSIBLE_HOME=" + t.TempDir()}}

	result, err := Run(context.Background(), local, spec, io.Discard)
	if err != nil || result.Report.Status != StatusPass || len(result.Report.Checks) != 2 {
		t.Fatalf("healthy state must pass: %+v %v", result, err)
	}

	preserve = false
	result, err = Run(context.Background(), local, spec, io.Discard)
	var rejected *RejectedError
	if !errors.As(err, &rejected) || result.Report.Checks[len(result.Report.Checks)-1].ID != "host.header-preserved" {
		t.Fatalf("broken state must be an explicit rejection: %+v %v", result, err)
	}

	server.Close() // nothing listens: still a symptom (fail), not a technical error
	_, err = Run(context.Background(), local, spec, io.Discard)
	if !errors.As(err, &rejected) {
		t.Fatalf("unreachable application is a failed check: %v", err)
	}

	spec.ExtraArgs = []string{"-e", `{"verification_url":"htp://broken","ansible_python_interpreter":"` + filepath.Join(ansibleBin, "python") + `"}`}
	_, err = Run(context.Background(), local, spec, io.Discard)
	var technical *TechnicalError
	if !errors.As(err, &technical) || errors.As(err, &rejected) {
		t.Fatalf("an unusable verifier must be a technical error: %v", err)
	}
}
