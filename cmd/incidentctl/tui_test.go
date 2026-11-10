package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/exercises"
	"incidentctl/internal/remote"
	"incidentctl/internal/vm"
)

func TestDiscoverExercisesLoadsDescriptionAndTips(t *testing.T) {
	t.Parallel()
	items, err := exercises.NewLocalSource(filepath.Join("..", "..", exerciseRoot)).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one exercise")
	}
	item := items[0]
	if item.Err != nil {
		t.Fatalf("exercise is invalid: %v", item.Err)
	}
	if item.Description == "" || len(item.Tips) == 0 {
		t.Fatalf("description or tips were not loaded: description=%t tips=%d", item.Description != "", len(item.Tips))
	}
	if filepath.Base(item.Dir) != item.Exercise.Name {
		t.Fatalf("directory %q does not match exercise %q", item.Dir, item.Exercise.Name)
	}
	if item.Exercise.Name == "skills" {
		t.Fatal("skills directory must not be treated as an exercise")
	}
}

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
