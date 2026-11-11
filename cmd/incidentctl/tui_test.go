package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"incidentctl/internal/config"
	"incidentctl/internal/identity"
	"incidentctl/internal/remote"
	"incidentctl/internal/vm"
)

func TestExpectedVerificationFailureRequiresPlaybookExit(t *testing.T) {
	t.Parallel()
	expected := &playbookRunError{Phase: "execute playbook", Err: &remote.CommandError{ExitCode: 2}}
	if !isExpectedVerificationFailure(expected) {
		t.Fatal("playbook failure must be accepted as the expected verification result")
	}
	for _, err := range []error{
		&playbookRunError{Phase: "upload exercise", Err: &remote.CommandError{ExitCode: 2}},
		&playbookRunError{Phase: "execute playbook", Err: errors.New("ssh transport")},
		&playbookRunError{Phase: "execute playbook", Err: &remote.CommandError{ExitCode: 255}},
	} {
		if isExpectedVerificationFailure(err) {
			t.Fatalf("unexpected failure was accepted: %v", err)
		}
	}
}

func TestSSHCommandIncludesConnectionSettings(t *testing.T) {
	t.Parallel()
	command := sshCommand(vm.Connection{
		Host:                  "vm.example",
		Port:                  2222,
		User:                  "root",
		IdentityFile:          "/tmp/key with space",
		StrictHostKeyChecking: true,
	})
	for _, part := range []string{"ssh", "-p 2222", "'/tmp/key with space'", "StrictHostKeyChecking=yes", "root@vm.example"} {
		if !strings.Contains(command, part) {
			t.Fatalf("ssh command %q does not contain %q", command, part)
		}
	}
}

func TestTUIStoresTokenWithoutAffectingExerciseState(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token.json")
	provider := vm.NewMicroVMServiceProvider(config.MicroVMServiceConfig{BaseURL: "https://microvm.example"})
	model := tuiModel{
		provider:    provider,
		tokenFile:   tokenFile,
		tokenReturn: screenExercises,
		screen:      screenToken,
	}

	updated, _ := model.updateTokenKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret-token")})
	updated, _ = updated.(tuiModel).updateTokenKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.screen != screenExercises || got.token != "secret-token" {
		t.Fatalf("unexpected TUI token state: screen=%v token=%q", got.screen, got.token)
	}
	record, err := identity.LoadToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if record.Token != "secret-token" || record.InstalledAt <= 0 {
		t.Fatalf("unexpected persisted token: %+v", record)
	}
}
