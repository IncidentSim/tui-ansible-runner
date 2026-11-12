package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
	"incidentctl/internal/verifier"
)

// playbookClient is the SSH boundary used by playbook execution and
// verification; *remote.Client implements it and tests substitute a fake.
type playbookClient interface {
	verifier.Remote
	RunShellInput(script string, input io.Reader, args ...string) error
	RunScript(script string, args ...string) error
}

// verifyExercise is the single verification entry point for CLI, TUI and the
// audit flow. Manifest exercises run through the structured report engine;
// legacy exercises keep the playbook exit-code contract.
//
// A nil error means passed. *verifier.RejectedError is an explicit failed
// check. Everything else is a technical error.
func verifyExercise(ctx context.Context, client playbookClient, remoteWorkDir string, exercise config.Exercise, dir string, extra []string, out io.Writer) (*verifier.Result, error) {
	if exercise.Verification.Report == nil {
		playbook := exercise.Verification.Playbook
		if playbook == "" {
			playbook = exercise.Playbooks.Verification
		}
		return nil, runPlaybook(client, remoteWorkDir, exercise, dir, playbook, extra)
	}
	return verifier.Run(ctx, client, verifier.Spec{
		Name:           exercise.Name,
		ExerciseDir:    dir,
		CollectionsDir: filepath.Join(exercise.Root, "collections"),
		WorkDir:        remoteWorkDir,
		Playbook:       exercise.Verification.Playbook,
		Timeout:        exercise.Verification.Timeout,
		Required:       exercise.Verification.Report.Checks,
		ExtraArgs:      extra,
	}, out)
}

func isVerdict(err error) bool {
	var rejected *verifier.RejectedError
	return errors.As(err, &rejected)
}

func isTechnicalVerification(err error) bool {
	var technical *verifier.TechnicalError
	return errors.As(err, &technical)
}

var _ playbookClient = (*remote.Client)(nil)
