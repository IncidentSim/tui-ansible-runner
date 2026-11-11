package main

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/config"
)

func TestRemoteBashScriptsHaveValidSyntax(t *testing.T) {
	t.Parallel()
	scripts := map[string]string{
		"ssh-check":     sshCheckScript,
		"connection":    connectionIPScript,
		"bootstrap":     bootstrapScript,
		"upload":        uploadScript,
		"playbook":      playbookScript,
		"audit-install": auditInstallScript,
		"audit-control": auditControlScript,
	}
	for name, script := range scripts {
		name, script := name, script
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash syntax error: %v\n%s", err, output)
			}
		})
	}
}

func TestSelectPlaybook(t *testing.T) {
	t.Parallel()
	exercise := config.Exercise{Playbooks: config.ExerciseActions{
		Infra: "infra/playbook.yml", Incident: "incident/playbook.yml", Verification: "verification/playbook.yml",
	}}
	for command, want := range map[string]string{
		"infra": "infra/playbook.yml", "incident": "incident/playbook.yml", "verify": "verification/playbook.yml",
	} {
		got, err := selectPlaybook(exercise, command)
		if err != nil || got != want {
			t.Fatalf("selectPlaybook(%q) = %q, %v; want %q", command, got, err, want)
		}
	}
}

func TestMakeArchiveUsesExerciseRelativeNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "infra", "playbook.yml"), []byte("---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := makeArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	var names []string
	for {
		header, err := r.Next()
		if err != nil {
			break
		}
		names = append(names, header.Name)
	}
	if got := strings.Join(names, ","); got != "infra,infra/playbook.yml" {
		t.Fatalf("archive names = %q", got)
	}
}
