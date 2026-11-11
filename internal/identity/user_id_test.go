package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePersistsStableUserID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "incidentctl", "user-id")

	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("user ID changed: first=%q second=%q", first, second)
	}
	if !userIDPattern.MatchString(first) {
		t.Fatalf("generated ID has an unexpected format: %q", first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("user ID file permissions = %o, want 600", got)
	}
}

func TestLoadOrCreateRejectsInvalidExistingID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "user-id")
	if err := os.WriteFile(path, []byte("not-an-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("invalid existing user ID was accepted")
	}
}
