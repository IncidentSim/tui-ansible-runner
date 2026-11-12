package exercises

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const catalogManifest = `schema_version: 1
id: manifest-one
description: Manifest exercise
environment: vm.json
actions:
  provision:
    playbook: infra/playbook.yml
  inject:
    playbook: incident/playbook.yml
verification:
  playbook: verification/playbook.yml
  timeout: 2m
  report:
    schema: incidentsim.verify/v1
    checks: [only.check]
`

func TestListCatalogSupportsLegacyAndManifestExercises(t *testing.T) {
	root := t.TempDir()
	writeExercise(t, root, "legacy-one")
	writeExercise(t, root, "manifest-one")
	if err := os.WriteFile(filepath.Join(root, "manifest-one", "manifest.yaml"), []byte(catalogManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory with neither vm.json nor manifest.yaml is not an exercise.
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	items, err := listCatalog(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Err != nil {
			t.Fatalf("%s: %v", item.Dir, item.Err)
		}
	}
	if items[0].Exercise.Manifest || !items[1].Exercise.Manifest || items[1].Exercise.Verification.Report == nil {
		t.Fatalf("unexpected contracts: %+v / %+v", items[0].Exercise.Verification, items[1].Exercise.Verification)
	}
}

func TestListCatalogReportsBrokenManifestWithoutVMJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("schema_version: 1\nid: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := listCatalog(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Err == nil {
		t.Fatalf("a broken manifest must surface as an item error: %+v", items)
	}
}
