package main

import (
	"os"
	"path/filepath"
	"testing"

	"incidentctl/internal/exercises"
	"incidentctl/internal/vm"
)

func TestBuildExerciseSourceSelectsLocal(t *testing.T) {
	t.Parallel()
	source, err := buildExerciseSource("local", t.TempDir(), "unused.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := source.(*exercises.LocalSource); !ok {
		t.Fatalf("unexpected source type %T", source)
	}
}

func TestBuildConnectionProviderSelectsMicroVMStub(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "microvm.json")
	data := []byte(`{
  "base_url": "https://microvm.example/api",
  "token_env": "INCIDENTCTL_MICROVM_TOKEN",
  "timeout_seconds": 30,
  "remote_work_dir": "/var/tmp/incidentctl/exercises"
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	provider, workDir, err := buildConnectionProvider("microvm-service", "unused.json", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*vm.MicroVMServiceProvider); !ok {
		t.Fatalf("unexpected provider type %T", provider)
	}
	if workDir != "/var/tmp/incidentctl/exercises" {
		t.Fatalf("unexpected work directory %q", workDir)
	}
}
