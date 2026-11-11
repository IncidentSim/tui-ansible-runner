package vmstate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"incidentctl/internal/vm"
)

func TestStorePersistsVMAndReopensSSHConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm-state.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveVM("nginx-wrong-api", "vm-123"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSSH("nginx-wrong-api", vm.Connection{
		Host:       "vm.example",
		Port:       2222,
		User:       "root",
		PrivateKey: "PRIVATE KEY",
		Bastion:    &vm.Bastion{Host: "bastion.example", Port: 22, User: "jump"},
	}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %o, want 600", info.Mode().Perm())
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := reloaded.Lookup("nginx-wrong-api")
	if !ok || record.VMID != "vm-123" || record.SSH == nil {
		t.Fatalf("unexpected saved record: %+v, found=%v", record, ok)
	}
	connection, cleanup, err := reloaded.OpenConnection("nginx-wrong-api")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if connection.Host != "vm.example" || connection.Port != 2222 || connection.User != "root" {
		t.Fatalf("unexpected connection: %+v", connection)
	}
	if connection.Bastion == nil || connection.Bastion.Host != "bastion.example" {
		t.Fatalf("unexpected bastion: %+v", connection.Bastion)
	}
	key, err := os.ReadFile(connection.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "PRIVATE KEY" {
		t.Fatalf("saved key = %q", key)
	}

	cleanup()
	if _, err := os.Stat(connection.IdentityFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary key still exists: %v", err)
	}
}

func TestStoreClearRemovesSavedVMs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm-state.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveVM("exercise", "vm-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Lookup("exercise"); ok {
		t.Fatal("cleared state still contains the exercise")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Lookup("exercise"); ok {
		t.Fatal("cleared state was persisted with the exercise")
	}
}
