package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"nginx-wrong-api", "web-01", "exercise.example_1"} {
		if !SafeName(value) {
			t.Fatalf("expected %q to be safe", value)
		}
	}
	for _, value := range []string{"", "../vm", "has space", "/absolute"} {
		if SafeName(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestConfigValidateRejectsRootWorkDirectory(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Paths.RemoteWorkDir = "/"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected root work directory to be rejected")
	}
}

func TestExerciseValidateRejectsTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exercise := validExercise()
	exercise.Playbooks.Incident = "../incident.yml"
	if err := exercise.Validate(dir); err == nil {
		t.Fatal("expected a playbook outside the exercise to be rejected")
	}
}

func TestExerciseValidateRejectsDuplicatePackages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePlaybooks(t, dir)
	exercise := validExercise()
	exercise.Runtime.BootstrapPackages = append(exercise.Runtime.BootstrapPackages, exercise.Runtime.BootstrapPackages[0])
	if err := exercise.Validate(dir); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate package error, got %v", err)
	}
}

func TestMicroVMServiceConfigValidate(t *testing.T) {
	t.Parallel()
	valid := MicroVMServiceConfig{
		BaseURL:        "https://microvm.example/api",
		TokenEnv:       "INCIDENTCTL_MICROVM_TOKEN",
		TimeoutSeconds: 30,
		RemoteWorkDir:  "/var/tmp/incidentctl/exercises",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config was rejected: %v", err)
	}
	invalid := valid
	invalid.BaseURL = "microvm.example"
	if err := invalid.Validate(); err == nil {
		t.Fatal("relative service URL must be rejected")
	}
}

func TestExerciseRepositoryConfigRejectsTraversal(t *testing.T) {
	t.Parallel()
	cfg := ExerciseRepositoryConfig{
		Repository:   "git@example.invalid:incidents.git",
		Ref:          "main",
		Subdirectory: "../private",
		CacheDir:     filepath.Join(t.TempDir(), "cache"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("repository subdirectory traversal must be rejected")
	}
}

func validConfig(t *testing.T) Config {
	t.Helper()
	key := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(key, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		Target: TargetConfig{Host: "vm.example", Port: 22, User: "root", IdentityFile: key},
		Paths:  PathsConfig{RemoteWorkDir: "/var/tmp/incidentctl/exercises"},
	}
}

func validExercise() Exercise {
	return Exercise{
		SchemaVersion: 1,
		Name:          "nginx-wrong-api",
		Resources:     Resources{VCPUCount: 1, MemoryMiB: 512},
		Runtime: Runtime{
			OS:                OperatingSystem{ID: "ubuntu", Version: "24.04"},
			BootstrapPackages: []Package{{Name: "ansible-core", Version: "2.16.3"}},
		},
		Playbooks: ExerciseActions{
			Infra:        "infra/playbook.yml",
			Incident:     "incident/playbook.yml",
			Verification: "verification/playbook.yml",
		},
	}
}

func writePlaybooks(t *testing.T, dir string) {
	t.Helper()
	for _, rel := range []string{"infra/playbook.yml", "incident/playbook.yml", "verification/playbook.yml"} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
