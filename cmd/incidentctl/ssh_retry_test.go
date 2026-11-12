package main

import (
	"strings"
	"testing"

	"incidentctl/internal/remote"
	"incidentctl/internal/vm"
)

func TestInfraHandshakeFailureRetainsConnectionAndRetriesOnlyFailedStage(t *testing.T) {
	cleaned := false
	run := &solutionRun{instance: vm.VirtualMachine{ID: "same-vm"}, connection: vm.Connection{Host: "vm.example"}, cleanup: func() { cleaned = true }}
	model := tuiModel{screen: screenSolution, solution: run, busy: true}
	for stage := solutionStage(0); stage < solutionApplyInfra; stage++ {
		model.solutionDone[stage] = true
	}
	err := &playbookRunError{Phase: "execute playbook", Err: &remote.CommandError{ExitCode: 255, BeforeCommand: true, Attempts: 3}}
	updated, _ := model.Update(solutionStageResult{run: run, stage: solutionApplyInfra, err: err})
	failed := updated.(tuiModel)
	if cleaned || failed.connection == nil || !failed.solutionCanRetry || !strings.Contains(failed.status, "Нажмите p") {
		t.Fatalf("lost retryable VM/SSH access: %s", failed.status)
	}
	updated, command := failed.updateKey("p")
	retrying := updated.(tuiModel)
	if command == nil || !retrying.busy || retrying.solutionStage != solutionApplyInfra || run.instance.ID != "same-vm" {
		t.Fatal("retry must resume infra on the existing VM")
	}
	for stage := solutionStage(0); stage < solutionApplyInfra; stage++ {
		if !retrying.solutionDone[stage] {
			t.Fatalf("retry reset a completed stage: %v", stage)
		}
	}
	retrying.releaseSSH()
	if !cleaned {
		t.Fatal("retained SSH identity was not cleaned up on release")
	}
}

func TestInfraFailureAfterCommandStartCannotBeReplayedWithP(t *testing.T) {
	run := &solutionRun{instance: vm.VirtualMachine{ID: "same-vm"}}
	model := tuiModel{screen: screenSolution, solution: run, busy: true}
	err := &playbookRunError{Phase: "execute playbook", Err: &remote.CommandError{ExitCode: 255}}
	updated, _ := model.Update(solutionStageResult{run: run, stage: solutionApplyInfra, err: err})
	failed := updated.(tuiModel)
	if failed.solutionCanRetry {
		t.Fatal("unknown execution outcome must not allow replay")
	}
	_, command := failed.updateKey("p")
	if command != nil {
		t.Fatal("p replayed a possibly executed playbook")
	}
}
