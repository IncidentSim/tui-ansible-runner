package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodManifest = `schema_version: 1
id: nginx-wrong-api
description: Nginx drops the Host header
environment: vm.json
actions:
  provision:
    playbook: infra/playbook.yml
  inject:
    playbook: incident/playbook.yml
verification:
  playbook: verification/playbook.yml
  timeout: 5m
  report:
    schema: incidentsim.verify/v1
    checks: [host.header, app.reachable]
`

func writeExerciseDir(t *testing.T, manifest string, withPlaybooksInVM bool) (root, name string) {
	t.Helper()
	root = t.TempDir()
	name = "nginx-wrong-api"
	dir := filepath.Join(root, name)
	writePlaybooks(t, dir)
	vm := validExercise()
	if !withPlaybooksInVM {
		vm.Playbooks = ExerciseActions{}
	}
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, name
}

func TestLoadExerciseLegacyVMJSONOnly(t *testing.T) {
	t.Parallel()
	root, name := writeExerciseDir(t, "", true)
	exercise, _, err := LoadExercise(root, name)
	if err != nil {
		t.Fatal(err)
	}
	if exercise.Verification.Playbook != "verification/playbook.yml" || exercise.Verification.Report != nil {
		t.Fatalf("legacy verification contract changed: %+v", exercise.Verification)
	}
	if exercise.Root != root {
		t.Fatalf("root = %q", exercise.Root)
	}
}

func TestLoadExerciseManifest(t *testing.T) {
	t.Parallel()
	root, name := writeExerciseDir(t, goodManifest, true)
	exercise, _, err := LoadExercise(root, name)
	if err != nil {
		t.Fatal(err)
	}
	v := exercise.Verification
	if v.Timeout != 5*time.Minute || v.Report == nil || v.Report.Schema != "incidentsim.verify/v1" {
		t.Fatalf("verification = %+v", v)
	}
	if len(v.Report.Checks) != 2 || v.Report.Checks[0] != "host.header" {
		t.Fatalf("checks = %v", v.Report.Checks)
	}
	if exercise.Description != "Nginx drops the Host header" {
		t.Fatalf("description = %q", exercise.Description)
	}
}

func TestLoadExerciseManifestFillsPlaybooksMissingFromVMJSON(t *testing.T) {
	t.Parallel()
	root, name := writeExerciseDir(t, goodManifest, false)
	exercise, _, err := LoadExercise(root, name)
	if err != nil {
		t.Fatal(err)
	}
	if exercise.Playbooks.Infra != "infra/playbook.yml" || exercise.Playbooks.Incident != "incident/playbook.yml" ||
		exercise.Playbooks.Verification != "verification/playbook.yml" {
		t.Fatalf("playbooks = %+v", exercise.Playbooks)
	}
}

func TestLoadExerciseManifestRejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ from, to, want string }{
		"id mismatch":        {"id: nginx-wrong-api", "id: other", "id"},
		"unknown field":      {"description:", "surprise: 1\ndescription:", "surprise"},
		"missing timeout":    {"  timeout: 5m\n", "", "timeout"},
		"bad timeout":        {"timeout: 5m", "timeout: soon", "timeout"},
		"huge timeout":       {"timeout: 5m", "timeout: 25h", "timeout"},
		"unknown schema":     {"incidentsim.verify/v1", "other/v9", "schema"},
		"unsafe check id":    {"[host.header, app.reachable]", "['../x']", "check"},
		"duplicate check id": {"[host.header, app.reachable]", "[a, a]", "duplicate"},
		"no checks":          {"[host.header, app.reachable]", "[]", "check"},
		"env traversal":      {"environment: vm.json", "environment: ../vm.json", "environment"},
		"playbook mismatch":  {"playbook: infra/playbook.yml", "playbook: incident/playbook.yml", "playbook"},
		"playbook traversal": {"playbook: verification/playbook.yml", "playbook: ../x.yml", "playbook"},
		"bad version":        {"schema_version: 1", "schema_version: 2", "schema_version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(goodManifest, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			manifest := strings.Replace(goodManifest, tc.from, tc.to, 1)
			root, exercise := writeExerciseDir(t, manifest, true)
			_, _, err := LoadExercise(root, exercise)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadExerciseManifestRejectsTrailingDocument(t *testing.T) {
	t.Parallel()
	root, name := writeExerciseDir(t, goodManifest+"---\nid: x\n", true)
	if _, _, err := LoadExercise(root, name); err == nil {
		t.Fatal("multi-document manifest must be rejected")
	}
}
