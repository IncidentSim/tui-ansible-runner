package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"incidentctl/internal/config"
	"incidentctl/internal/identity"
	"incidentctl/internal/remote"
	"incidentctl/internal/vm"
	"incidentctl/internal/vmstate"
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

func TestAuditStartMustSucceedBeforeTerminalStage(t *testing.T) {
	run := &solutionRun{}
	model := tuiModel{solution: run, busy: true, solutionStage: solutionVerifyIncident}
	updated, cmd := model.Update(solutionStageResult{run: run, stage: solutionVerifyIncident})
	model = updated.(tuiModel)
	if model.solutionStage != solutionStartAudit || cmd == nil {
		t.Fatal("incident confirmation must be followed by recording start")
	}
	cleaned := false
	run.cleanup = func() { cleaned = true }
	failed, cmd := model.Update(solutionStageResult{run: run, stage: solutionStartAudit, err: errors.New("audit unavailable")})
	failure := failed.(tuiModel)
	if cmd != nil || failure.busy || !failure.solutionFailed || failure.connection != nil || !cleaned {
		t.Fatal("failed recording must block terminal and release SSH credentials")
	}
	updated, cmd = model.Update(solutionStageResult{run: run, stage: solutionStartAudit})
	if updated.(tuiModel).solutionStage != solutionOpenTerminal || cmd == nil {
		t.Fatal("successful recording start must allow terminal creation")
	}
}

func TestVerificationFallbackWarningRemainsVisible(t *testing.T) {
	for _, err := range []error{nil, errors.New("Ansible failed")} {
		model := tuiModel{busy: true}
		updated, _ := model.Update(operationResult{kind: operationVerify, err: err, warning: "LLM не удалась; использован Ansible"})
		got := updated.(tuiModel)
		if !strings.Contains(got.status, "LLM не удалась") {
			t.Fatalf("warning hidden: %s", got.status)
		}
		if (err == nil) != strings.Contains(got.status, "успешно") {
			t.Fatalf("wrong validation status: %s", got.status)
		}
	}
}

func TestRejectedSolutionIsDistinctFromTechnicalFailure(t *testing.T) {
	for _, tc := range []struct {
		phase    string
		code     int
		rejected bool
	}{
		{"execute playbook", 2, true},
		{"execute playbook", 255, false},
		{"execute playbook", 4, false},
		{"upload exercise", 2, false},
	} {
		model := tuiModel{}
		updated, _ := model.Update(operationResult{kind: operationVerify,
			err: &playbookRunError{Phase: tc.phase, Err: &remote.CommandError{ExitCode: tc.code}}})
		status := updated.(tuiModel).status
		if strings.HasPrefix(status, "Решение пока не прошло") != tc.rejected {
			t.Fatalf("phase=%s code=%d: %s", tc.phase, tc.code, status)
		}
		if strings.HasPrefix(status, "Ошибка:") == tc.rejected {
			t.Fatalf("wrong technical error status: %s", status)
		}
	}
}

type reconnectTestProvider struct {
	vm.APIProvider
	requestedVM string
}

func (p *reconnectTestProvider) SSHConnection(_ context.Context, id string) (vm.Connection, func(), error) {
	p.requestedVM = id
	return vm.Connection{}, nil, &remote.CommandError{ExitCode: 255}
}

func TestIncidentRetryRefreshesSSHForSameVMWithoutReprovisioning(t *testing.T) {
	provider := &reconnectTestProvider{}
	run := &solutionRun{provider: provider, instance: vm.VirtualMachine{ID: "existing-vm"}}
	model := tuiModel{screen: screenSolution, solution: run, solutionFailed: true, solutionFailure: solutionVerifyIncident}
	model.solutionDone[solutionApplyInfra] = true
	model.solutionDone[solutionApplyIncident] = true
	updated, cmd := model.updateKey("p")
	retrying := updated.(tuiModel)
	if !retrying.busy || retrying.solutionFailed || retrying.solutionStage != solutionVerifyIncident {
		t.Fatal("retry must resume only incident verification")
	}
	if !retrying.solutionDone[solutionApplyInfra] || !retrying.solutionDone[solutionApplyIncident] {
		t.Fatal("retry reset completed provisioning stages")
	}
	commands := cmd().(tea.BatchMsg)
	result := commands[0]().(solutionStageResult)
	if provider.requestedVM != "existing-vm" || result.stage != solutionVerifyIncident {
		t.Fatalf("reconnected to wrong VM/stage: %s %v", provider.requestedVM, result.stage)
	}
	failed, next := retrying.Update(result)
	if next != nil || !strings.Contains(failed.(tuiModel).status, "Ошибка SSH") || !strings.Contains(failed.(tuiModel).status, "existing-vm") {
		t.Fatalf("SSH failure was misclassified: %s", failed.(tuiModel).status)
	}
}

func TestNewRecordingKeyRemovedFromSolutionScreen(t *testing.T) {
	model := tuiModel{screen: screenSolution, connection: &vm.Connection{Host: "vm.example"}}
	updated, cmd := model.updateKey("r")
	if cmd != nil || updated.(tuiModel).busy {
		t.Fatal("r must no longer start a recording")
	}
	if strings.Contains(model.viewSolution(150), "новая запись") {
		t.Fatal("obsolete recording key is visible")
	}
}

func TestSavedVMReuseKeepsAuditPreparationStages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm-state.json")
	if err := os.WriteFile(path, []byte(`{"vms":{"exercise":{"vm_id":"saved-vm"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := vmstate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	run := &solutionRun{state: state, item: exerciseItem{exercise: config.Exercise{Name: "exercise"}}}
	model := tuiModel{solution: run, busy: true}
	updated, command := model.Update(solutionStageResult{run: run, stage: solutionCheckCapabilities})
	model = updated.(tuiModel)
	if model.solutionStage != solutionFindVM {
		t.Fatal("saved VM lookup was skipped")
	}
	result := command().(solutionStageResult)
	updated, _ = model.Update(result)
	model = updated.(tuiModel)
	if run.instance.ID != "saved-vm" || model.solutionStage != solutionConnectSSH {
		t.Fatal("existing VM was not reused")
	}
	updated, _ = model.Update(solutionStageResult{run: run, stage: solutionConnectSSH})
	model = updated.(tuiModel)
	if model.solutionStage != solutionPrepareAnsible {
		t.Fatal("Ansible preparation was skipped")
	}
	updated, _ = model.Update(solutionStageResult{run: run, stage: solutionPrepareAnsible})
	if updated.(tuiModel).solutionStage != solutionPrepareAudit {
		t.Fatal("reused VM skipped audit installation")
	}
}

type mergeSSHProvider struct {
	vm.APIProvider
	cleanup func()
}

func (p *mergeSSHProvider) SSHConnection(context.Context, string) (vm.Connection, func(), error) {
	return vm.Connection{Host: "vm.example", PrivateKey: "test fixture"}, p.cleanup, nil
}

func TestRefreshSSHRemovesTemporaryKeyIfStateCannotBeSaved(t *testing.T) {
	state, err := vmstate.Load(filepath.Join(t.TempDir(), "vm-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	run := &solutionRun{state: state, provider: &mergeSSHProvider{cleanup: func() { cleaned = true }}, item: exerciseItem{exercise: config.Exercise{Name: "exercise"}}}
	// No VM record exists, so saving refreshed SSH data must fail before adoption.
	if err := run.refreshSSH(); err == nil || !cleaned || run.client != nil {
		t.Fatal("failed SSH persistence leaked a temporary key or adopted the connection")
	}
}
