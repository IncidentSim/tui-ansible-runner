package identity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestSaveTokenPersistsTokenAndUnixInstallationDate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "incidentctl", "token.json")

	before := time.Now().Unix()
	record, err := SaveToken(path, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()
	if record.Token != "secret-token" || record.InstalledAt < before || record.InstalledAt > after {
		t.Fatalf("unexpected token record: %+v", record)
	}
	loaded, err := LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != record {
		t.Fatalf("loaded token record = %+v, want %+v", loaded, record)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("token file permissions = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["installed_at"].(float64); !ok {
		t.Fatalf("token file does not contain Unix installed_at: %s", data)
	}
}

func TestLoadTokenReportsMissingFile(t *testing.T) {
	t.Parallel()
	_, err := LoadToken(filepath.Join(t.TempDir(), "missing-token.json"))
	if !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}
