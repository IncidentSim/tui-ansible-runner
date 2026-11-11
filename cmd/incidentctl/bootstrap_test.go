package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"incidentctl/internal/vm"
)

func TestBuildRuntimeUsesUnifiedConfiguration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "incidentctl.json")
	userIDPath := filepath.Join(t.TempDir(), "user-id")
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	data := []byte(fmt.Sprintf(`{
  "exercise_repository": {
    "repository": "git@example.invalid:incidents.git",
    "ref": "master",
    "subdirectory": ".",
    "cache_dir": "/tmp/incidentctl-test-cache"
  },
  "vm_service": {
    "base_url": "https://microvm.example/api",
    "token_env": "INCIDENTCTL_MICROVM_TOKEN",
    "timeout_seconds": 30,
    "remote_work_dir": "/var/tmp/incidentctl/exercises"
  },
  "user_id_file": %q,
  "token_file": %q
}`, userIDPath, tokenPath))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	source, provider, workDir, gotTokenPath, err := buildRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	if source == nil || provider == nil {
		t.Fatal("unified configuration did not build runtime")
	}
	if _, ok := provider.(*vm.MicroVMServiceProvider); !ok {
		t.Fatalf("unexpected provider type %T", provider)
	}
	providerStub := provider.(*vm.MicroVMServiceProvider)
	if providerStub.UserID() == "" {
		t.Fatal("runtime did not load a user ID")
	}
	if _, err := os.Stat(userIDPath); err != nil {
		t.Fatalf("user ID file was not created: %v", err)
	}
	if workDir != "/var/tmp/incidentctl/exercises" {
		t.Fatalf("unexpected work directory %q", workDir)
	}
	if gotTokenPath != tokenPath {
		t.Fatalf("unexpected token path %q", gotTokenPath)
	}
}
