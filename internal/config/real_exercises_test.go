package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Every shipped exercise in the exersice submodule must load through the
// manifest path with a structured report contract.
func TestShippedExercisesLoadWithManifests(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "exersice"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "nginx-wrong-api", "manifest.yaml")); err != nil {
		t.Skip("exersice submodule is not populated with manifests")
	}
	names := []string{"nginx-wrong-api", "postgres-bad-config", "postgres-conn-leak", "postgres-disk-full",
		"postgres-hba-reject", "postgres-lock", "postgres-missing-index", "postgres-wrong-port"}
	for _, name := range names {
		exercise, _, err := LoadExercise(root, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !exercise.Manifest || exercise.Verification.Report == nil || len(exercise.Verification.Report.Checks) == 0 || exercise.Verification.Timeout <= 0 {
			t.Fatalf("%s: incomplete verification contract %+v", name, exercise.Verification)
		}
	}
}
