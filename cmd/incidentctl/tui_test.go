package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
)

func TestDiscoverExercisesLoadsDescriptionAndTips(t *testing.T) {
	t.Parallel()
	items, err := discoverExercises(filepath.Join("..", "..", exerciseRoot))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one exercise")
	}
	item := items[0]
	if item.err != nil {
		t.Fatalf("exercise is invalid: %v", item.err)
	}
	if item.description == "" || len(item.tips) == 0 {
		t.Fatalf("description or tips were not loaded: description=%t tips=%d", item.description != "", len(item.tips))
	}
	if filepath.Base(item.dir) != item.exercise.Name {
		t.Fatalf("directory %q does not match exercise %q", item.dir, item.exercise.Name)
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
	command := sshCommand(config.TargetConfig{
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

func TestMarkdownToTextRemovesHeadingMarkers(t *testing.T) {
	t.Parallel()
	got := markdownToText("# Заголовок\n\nТекст\n\n```bash\necho ok\n```\n")
	if strings.Contains(got, "# Заголовок") || !strings.Contains(got, "Заголовок") || !strings.Contains(got, "echo ok") {
		t.Fatalf("unexpected markdown conversion: %q", got)
	}
}
