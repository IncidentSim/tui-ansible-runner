package exercises

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"incidentctl/internal/config"
)

func TestLocalSourceListsExercisesAndIgnoresMetadataDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeExercise(t, root, "web-incident")
	if err := os.MkdirAll(filepath.Join(root, "skills", "authoring"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Catalog\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := NewLocalSource(root).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Exercise.Name != "web-incident" {
		t.Fatalf("unexpected exercises: %+v", items)
	}
	if items[0].Description != "Web incident" || len(items[0].Tips) != 1 {
		t.Fatalf("description or tips were not loaded: %+v", items[0])
	}
}

func TestGitSourceLoadsCatalogFromLocalRepository(t *testing.T) {
	repository := t.TempDir()
	writeExercise(t, filepath.Join(repository, "exersice"), "git-incident")
	runTestGit(t, repository, "init", "-b", "main")
	runTestGit(t, repository, "add", ".")
	runTestGit(t, repository, "commit", "-m", "add exercise")

	source := NewGitSource(config.ExerciseRepositoryConfig{
		Repository:   repository,
		Ref:          "main",
		Subdirectory: "exersice",
		CacheDir:     filepath.Join(t.TempDir(), "cache"),
	})
	items, err := source.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Exercise.Name != "git-incident" {
		t.Fatalf("unexpected Git exercises: %+v", items)
	}
}

func writeExercise(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	for _, path := range []string{"infra/playbook.yml", "incident/playbook.yml", "verification/playbook.yml"} {
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte("---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "tips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Web incident\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tips", "tip1.md"), []byte("# Inspect logs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := `{
  "schema_version": 1,
  "name": "` + name + `",
  "resources": {"vcpu_count": 1, "memory_mib": 512},
  "runtime": {
    "os": {"id": "ubuntu", "version": "24.04"},
    "bootstrap_packages": [{"name": "ansible-core", "version": "2.16.3"}],
    "managed_packages": []
  },
  "playbooks": {
    "infra": "infra/playbook.yml",
    "incident": "incident/playbook.yml",
    "verification": "verification/playbook.yml"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=incidentctl test",
		"GIT_AUTHOR_EMAIL=incidentctl@example.invalid",
		"GIT_COMMITTER_NAME=incidentctl test",
		"GIT_COMMITTER_EMAIL=incidentctl@example.invalid",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
